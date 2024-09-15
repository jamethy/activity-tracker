package main

import (
	"bufio"
	"bytes"
	"context"
	"embed"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/a-h/templ"
	"github.com/golang-jwt/jwt"
	"github.com/labstack/echo/v4"
	"golang.org/x/crypto/bcrypt"
	"io"
	"io/fs"
	"log"
	"log/slog"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-lambda-go/lambda"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

//go:embed static/*
var staticFiles embed.FS

type UserInfo struct {
	Username         string
	Password         string
	RestingHeartrate float64
	DateOfBirth      time.Time
}

const moderateFloorPercentage = 0.5
const highFloorPercentage = 0.7
const minimumModerateIntensityHoursPerWeek = 2.5

const jwtClaimsKey = "jwt-claims"
const userInfoFileName = "user-info.json"
const userDataFileName = "activity-tracker-data.csv"

// https://changelog.com/gotime/291
// https://templ.guide/
// https://github.com/a-h/templ
// https://htmx.org/
func main() {
	useLocalFile := flag.Bool("local-file", false, "use local instead of s3")
	runLocally := flag.Bool("run-locally", false, "run locally instead of lambda")
	initUser := flag.Bool("init-user", false, "run the initialize user command")

	flag.Parse()

	slog.SetDefault(setupLogger())

	slog.Info("start up config",
		"localFile", *useLocalFile,
		"runLocally", *runLocally,
	)

	getFileHandler := func(ctx context.Context, username, fileName string) (io.ReadWriteCloser, error) {
		if *useLocalFile {
			return &LocalFileData{
				ctx:      ctx,
				fileName: filepath.Join("localdata", username, fileName),
			}, nil
		}

		// else s3
		cfg, err := config.LoadDefaultConfig(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to load aws config: %w", err)
		}
		return &S3FileData{
			ctx:      ctx,
			s3Client: s3.NewFromConfig(cfg),
			bucket:   "activity-tracker-lambda-artifacts",
			key:      fmt.Sprintf("data/%s/%s", username, fileName),
		}, nil
	}

	if *initUser {
		userInfo, _ := userInfoFromIO(os.Stdin, os.Stdout)
		f, err := getFileHandler(context.Background(), userInfo.Username, userInfoFileName)
		if err != nil {
			log.Fatal(err)
		}
		defer safeClose(f, "init-user-info")
		err = json.NewEncoder(f).Encode(userInfo)
		if err != nil {
			log.Fatal(err)
		}

		f, err = getFileHandler(context.Background(), userInfo.Username, userDataFileName)
		if err != nil {
			log.Fatal(err)
		}
		defer safeClose(f, "init-user-data")

		// this creates the file if it doesn't exist, but doesn't erase any data
		b, _ := io.ReadAll(f)
		_, _ = f.Write(b)

		return
	}

	e := echo.New()
	e.Use(Recover())
	e.Use(RequestLogger())

	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if c.Path() == "/login" {
				return next(c)
			}
			s, err := c.Cookie("session")
			if err == nil {
				claims, jwtErr := parseJWT(s.Value)
				if jwtErr == nil {
					c.Set(jwtClaimsKey, claims)
					return next(c)
				}
				err = jwtErr
			}
			slog.Info("301 for user", "err", err.Error())
			return c.Redirect(http.StatusFound, "/login")
		}
	})

	e.GET("/login", func(c echo.Context) error {
		return render(c, page(loginForm()))
	})

	e.POST("/login", func(c echo.Context) error {
		var params struct {
			Username string `form:"username"`
			Password string `form:"password"`
		}
		// todo add validation
		if err := c.Bind(&params); err != nil {
			return err
		}
		infoFile, err := getFileHandler(c.Request().Context(), params.Username, userInfoFileName)
		if err != nil {
			slog.Warn("failed getting user info", "user", params.Username, "err", err)
			// assume it doesn't exist  for now
			return c.NoContent(http.StatusUnauthorized)
		}
		defer safeClose(infoFile, "login")
		var userInfo UserInfo
		err = json.NewDecoder(infoFile).Decode(&userInfo)
		if err != nil {
			slog.Warn("failed parsing user info", "user", params.Username, "err", err)
			return c.NoContent(http.StatusInternalServerError)
		}

		err = bcrypt.CompareHashAndPassword([]byte(userInfo.Password), []byte(params.Password))
		if errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) {
			return c.NoContent(http.StatusUnauthorized)
		}
		if err != nil {
			slog.Warn("failed comparing password", "user", params.Username, "err", err)
			return c.NoContent(http.StatusUnauthorized)
		}

		// https://echo.labstack.com/docs/cookies
		value, exp, err := issueJWT(userInfo)
		if err != nil {
			return err
		}
		c.SetCookie(&http.Cookie{
			Name:    "session",
			Value:   value,
			Expires: exp,
		})
		return c.Redirect(http.StatusFound, "/")
	})

	e.GET("", func(c echo.Context) error {
		claims := c.Get(jwtClaimsKey).(JWTClaims)
		f, err := getFileHandler(c.Request().Context(), claims.User, userDataFileName)
		if err != nil {
			return err
		}
		defer safeClose(f, "get data")

		days, err := readCSV(f)
		if err != nil {
			return fmt.Errorf("reading days: %w", err)
		}

		days = fillInDates(days, time.Now())
		summary := calcSummary(claims, days[:7])

		return render(c, page(mainContent(summarySection(summary), tracker(days, summary))))
	})

	e.POST("/entries", func(c echo.Context) error {

		var params struct {
			Date        string  `form:"date"`
			Duration    string  `form:"duration"`
			Effort      float32 `form:"effort"`
			Description string  `form:"description"`
		}

		if err := c.Bind(&params); err != nil {
			return fmt.Errorf("failed to marshal body")
		}

		claims := c.Get(jwtClaimsKey).(JWTClaims)
		f, err := getFileHandler(c.Request().Context(), claims.User, userDataFileName)
		if err != nil {
			return err
		}
		defer safeClose(f, "get data")

		date, err := time.Parse(time.DateOnly, params.Date)
		if err != nil {
			return c.NoContent(http.StatusNotAcceptable)
		}
		duration, err := time.ParseDuration(params.Duration)
		if err != nil {
			return c.NoContent(http.StatusNotAcceptable)
		}

		entry := DayEntry{
			Duration:    duration,
			Effort:      params.Effort,
			Description: params.Description,
		}

		err = addCSVEntries([]DayLog{{
			Date:    date,
			Entries: []DayEntry{entry},
		}}, f)
		if err != nil {
			return err
		}
		return render(c, entryDisplay(entry))
	})

	e.GET("/add-entry-modal", func(c echo.Context) error {
		var params struct {
			Date string `query:"date"`
		}

		if err := c.Bind(&params); err != nil {
			return fmt.Errorf("failed to marshal body")
		}
		return render(c, addLogModal(params.Date))
	})

	e.POST("/logout", func(c echo.Context) error {
		c.SetCookie(&http.Cookie{
			Name:    "session",
			Value:   "invalid",
			Expires: time.Now().Add(-1 * time.Hour),
		})
		return c.Redirect(http.StatusFound, "/login")
	})

	fsys, err := fs.Sub(staticFiles, "static")
	if err != nil {
		panic(err)
	}
	fileHandler := http.FileServer(http.FS(fsys))
	e.GET("/styles.css", echo.WrapHandler(fileHandler))

	if *runLocally {
		slog.Info("Starting local execution")
		_ = e.Start(":8080")
	} else {
		slog.Info("Starting lambda execution")
		lambda.Start(LambdaEchoProxy(e))
	}

}

type Summary struct {
	RestingHeartRate           float64
	LowIntensitySum            time.Duration // better than nothing - worth fraction of moderate
	LowIntensityScore          float64
	ModerateIntensityHeartRate float64       // 50-70% of maximum heart rate, 93-130BPM
	ModerateIntensitySum       time.Duration // at least 2.5h/w
	ModerateIntensityScore     float64
	HighIntensityHeartRate     float64       // 70-85% of maximum, 130-158
	HighIntensitySum           time.Duration // at least 1.25h/w
	HighIntensityScore         float64
	ComboScore                 float64 // high intensity is about double time, 100 is goal
	RemainingModerateTime      time.Duration
	BonusLevel                 float64 // 5h equiv, 200%
}

func calcSummary(claims JWTClaims, days []DayLog) Summary {
	// https://www.heart.org/en/healthy-living/fitness/fitness-basics/aha-recs-for-physical-activity-in-adults
	age := time.Now().Sub(claims.DateOfBirth).Hours() / (24 * 365)
	maximumHeartRate := 206.09 - 0.67*age

	s := Summary{
		RestingHeartRate:           claims.RestingHeartrate,
		ModerateIntensityHeartRate: maximumHeartRate * moderateFloorPercentage,
		HighIntensityHeartRate:     maximumHeartRate * highFloorPercentage,
	}

	for _, d := range days {
		for _, e := range d.Entries {
			if e.Effort >= highFloorPercentage {
				s.HighIntensitySum += e.Duration
			} else if e.Effort >= moderateFloorPercentage {
				s.ModerateIntensitySum += e.Duration
			} else {
				s.LowIntensitySum += e.Duration
			}
		}
	}

	desiredModerateIntensityHours := minimumModerateIntensityHoursPerWeek * float64(len(days)/7.0)

	// calculate pseudo-hours
	s.LowIntensityScore = 0.2 * float64(s.LowIntensitySum.Hours())
	s.ModerateIntensityScore = 1.0 * float64(s.ModerateIntensitySum.Hours())
	s.HighIntensityScore = 2.0 * float64(s.HighIntensitySum.Hours())

	// calc remaining hours
	remainingMinutes := 60 * (desiredModerateIntensityHours - (s.LowIntensityScore + s.ModerateIntensityScore + s.HighIntensityScore))
	s.RemainingModerateTime = time.Duration(float64(time.Minute) * max(0, math.Floor(remainingMinutes)))

	// convert to score
	s.LowIntensityScore *= 100 / desiredModerateIntensityHours
	s.ModerateIntensityScore *= 100 / desiredModerateIntensityHours
	s.HighIntensityScore *= 100 / desiredModerateIntensityHours

	s.ComboScore = s.LowIntensityScore + s.ModerateIntensityScore + s.HighIntensityScore
	s.BonusLevel = 200

	return s
}

type (
	DayLog struct {
		Date    time.Time
		Entries []DayEntry
	}

	DayEntry struct {
		Duration    time.Duration
		Effort      float32
		Description string
	}
)

func render(c echo.Context, comp templ.Component) error {
	err := comp.Render(c.Request().Context(), c.Response())
	if err != nil {
		slog.Error("rendering component", "err", err)
		return c.HTML(http.StatusOK, "Error") // todo
	}
	return nil
}

func effortColor(e DayEntry) string {
	var color string
	switch {
	case e.Effort >= highFloorPercentage:
		color = "#009700FF"
	case e.Effort >= moderateFloorPercentage:
		color = "#296029"
	case e.Effort >= 0.2:
		color = "#3f4f3f"
	default:
		color = "#595e59"
	}
	return color
}

type LocalFileData struct {
	ctx      context.Context
	fileName string
	writer   io.WriteCloser
	reader   io.ReadCloser
}

func (l *LocalFileData) Read(p []byte) (n int, err error) {
	if l.reader == nil {
		slog.Info("opening local file")

		err = os.MkdirAll(filepath.Dir(l.fileName), 0755)
		if err != nil {
			return 0, fmt.Errorf("failed to mkdir -p: %w", err)
		}

		f, err := os.Open(l.fileName)
		if err != nil {
			return 0, fmt.Errorf("failed to open file for reading: %w", err)
		}
		l.reader = f
	}
	return l.reader.Read(p)
}

func (l *LocalFileData) Write(p []byte) (n int, err error) {
	if l.writer == nil {
		slog.Info("writing to local file")

		err = os.MkdirAll(filepath.Dir(l.fileName), 0755)
		if err != nil {
			return 0, fmt.Errorf("failed to mkdir -p: %w", err)
		}

		f, err := os.Create(l.fileName)
		if err != nil {
			return 0, fmt.Errorf("failed to open file for writing: %w", err)
		}
		l.writer = f
	}
	return l.writer.Write(p)
}

func (l *LocalFileData) Close() error {
	if l.reader != nil {
		return l.reader.Close()
	}
	if l.writer != nil {
		return l.writer.Close()
	}
	return nil
}

type S3FileData struct {
	ctx      context.Context
	s3Client *s3.Client
	bucket   string
	key      string

	writer io.ReadWriter
	reader io.ReadCloser
}

func (s *S3FileData) Read(p []byte) (n int, err error) {
	if s.reader == nil {
		result, err := s.s3Client.GetObject(s.ctx, &s3.GetObjectInput{
			Bucket: aws.String(s.bucket),
			Key:    aws.String(s.key),
		})
		if err != nil {
			return 0, fmt.Errorf("could not get object: %w", err)
		}
		s.reader = result.Body
	}
	return s.reader.Read(p)
}

func (s *S3FileData) Write(p []byte) (n int, err error) {
	if s.writer == nil {
		s.writer = &bytes.Buffer{}
	}
	return s.writer.Write(p)
}

func (s *S3FileData) Close() error {
	if s.writer != nil {
		_, err := s.s3Client.PutObject(s.ctx, &s3.PutObjectInput{
			Bucket: aws.String(s.bucket),
			Key:    aws.String(s.key),
			Body:   s.writer,
		})
		if err != nil {
			return fmt.Errorf("failed to write data to s3: %w", err)
		}
		s.writer = nil
	}
	if s.reader != nil {
		err := s.reader.Close()
		s.reader = nil
		if err != nil {
			return fmt.Errorf("failed to closer a reader: %w", err)
		}
	}
	return nil
}

func toCSVRecords(logs []DayLog) [][]string {
	records := make([][]string, 0)
	for _, l := range logs {
		for _, e := range l.Entries {
			records = append(records, []string{
				l.Date.Format(time.DateOnly),
				e.Duration.String(),
				fmt.Sprintf("%.2f", e.Effort),
				e.Description,
			})
		}
	}
	return records
}

func addCSVEntries(logs []DayLog, c io.ReadWriteCloser) error {
	contents, err := io.ReadAll(c)
	if err != nil {
		return fmt.Errorf("failed to read all of csv: %w", err)
	}

	b := bytes.NewBuffer(contents)

	records := toCSVRecords(logs)
	err = csv.NewWriter(b).WriteAll(records)
	if err != nil {
		return fmt.Errorf("failed to add to csv buffer: %w", err)
	}

	_, err = c.Write(b.Bytes())
	if err != nil {
		return fmt.Errorf("failed to add write to csv: %w", err)
	}
	return nil
}

func readCSV(file io.ReadCloser) ([]DayLog, error) {
	// Read the CSV
	records, err := csv.NewReader(file).ReadAll()
	if err != nil {
		return nil, fmt.Errorf("reading csv: %w", err)
	}

	entriesPerDay := make(map[string][]DayEntry)
	for i, r := range records {
		if len(r) != 4 {
			fmt.Println("incorrect number of columns for row ", i)
			continue
		}

		dateStr := r[0]
		_, err := time.Parse(time.DateOnly, dateStr)
		if err != nil {
			fmt.Println("error parsing date of row ", i, err)
			continue
		}

		duration, err := time.ParseDuration(r[1])
		if err != nil {
			fmt.Println("error parsing duration of row ", i, err)
			continue
		}

		effort, err := strconv.ParseFloat(r[2], 32)
		if err != nil {
			fmt.Println("error parsing effort of row ", i, err)
			continue
		}

		description := r[3]

		entriesPerDay[dateStr] = append(entriesPerDay[dateStr], DayEntry{
			Duration:    duration,
			Effort:      float32(effort),
			Description: description,
		})
	}

	var dayLogs []DayLog
	for k, v := range entriesPerDay {
		date, _ := time.Parse(time.DateOnly, k)
		dayLogs = append(dayLogs, DayLog{
			Date:    date,
			Entries: v,
		})
	}
	slices.SortFunc(dayLogs, func(a, b DayLog) int {
		if a.Date.Equal(b.Date) {
			return 0
		}
		if a.Date.After(b.Date) {
			return -1
		}
		return 1
	})

	// fill in dates

	return dayLogs, nil
}

func fillInDates(dayLogs []DayLog, upTo time.Time) []DayLog {
	// truncate to date
	upTo = time.Date(upTo.Year(), upTo.Month(), upTo.Day(), 0, 0, 0, 0, time.UTC)
	earliest := upTo.AddDate(0, 0, -13)
	if len(dayLogs) > 0 {
		last := dayLogs[len(dayLogs)-1].Date
		if last.Before(earliest) {
			earliest = last
		}
	}

	i := 0
	for d := upTo; !d.Before(earliest); d = d.AddDate(0, 0, -1) {
		if i >= len(dayLogs) {
			dayLogs = append(dayLogs, DayLog{Date: d})
		} else if !dayLogs[i].Date.Equal(d) {
			dayLogs = slices.Insert(dayLogs, i, DayLog{Date: d})
		}
		i++
	}

	return dayLogs
}

func safeClose(c io.Closer, name string) {
	err := c.Close()
	if err != nil {
		slog.Error("failed to close closer", "name", name, "err", err)
	}
}

type JWTClaims struct {
	User             string    `json:"user"`
	Expiration       int64     `json:"exp"`
	RestingHeartrate float64   `json:"heart"`
	DateOfBirth      time.Time `json:"dob"`
}

func (c JWTClaims) Valid() error {
	if c.User == "" {
		return errors.New("invalid user")
	}
	if c.Expiration == 0 {
		return errors.New("invalid exp")
	}
	return nil
}

func issueJWT(userInfo UserInfo) (string, time.Time, error) {
	exp := time.Now().AddDate(20, 0, 0)

	claims := JWTClaims{
		User:             userInfo.Username,
		Expiration:       exp.Unix(),
		RestingHeartrate: userInfo.RestingHeartrate,
		DateOfBirth:      userInfo.DateOfBirth,
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)

	jwtSecret := []byte(os.Getenv("JWT_SECRET"))
	tokenString, err := token.SignedString(jwtSecret)
	if err != nil {
		return "", exp, err
	}

	return tokenString, exp, nil
}

func parseJWT(tokenString string) (JWTClaims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &JWTClaims{}, func(token *jwt.Token) (any, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		jwtSecret := []byte(os.Getenv("JWT_SECRET"))
		return jwtSecret, nil
	})

	if err != nil {
		return JWTClaims{}, err
	}

	if claims, ok := token.Claims.(*JWTClaims); ok && token.Valid {
		return *claims, nil
	}

	return JWTClaims{}, fmt.Errorf("invalid token")
}

func userInfoFromIO(r io.Reader, w io.Writer) (UserInfo, error) {
	u := UserInfo{}

	buffedReader := bufio.NewReader(r)

	// todo input validation and error handling
	_, _ = w.Write([]byte("username: "))
	u.Username, _ = buffedReader.ReadString('\n')
	u.Username = strings.TrimSpace(u.Username)

	_, _ = w.Write([]byte("password: "))
	password, _ := buffedReader.ReadString('\n')
	password = strings.TrimSpace(password)
	passwordBytes, _ := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	u.Password = string(passwordBytes)

	_, _ = w.Write([]byte("resting heart rate: "))
	heartRateStr, _ := buffedReader.ReadString('\n')
	heartRateStr = strings.TrimSpace(heartRateStr)
	u.RestingHeartrate, _ = strconv.ParseFloat(heartRateStr, 10)

	for u.DateOfBirth.IsZero() {
		_, _ = w.Write([]byte(fmt.Sprintf("date of birth (%s): ", time.DateOnly)))
		dob, _ := buffedReader.ReadString('\n')
		dob = strings.TrimSpace(dob)
		u.DateOfBirth, _ = time.Parse(time.DateOnly, dob)
	}

	return u, nil
}

var version = "unknown" // filled in during goreleaser build

func setupLogger() *slog.Logger {
	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		AddSource:   false,
		Level:       slog.LevelDebug,
		ReplaceAttr: nil,
	})
	l := slog.New(h)
	l = l.With("app", slog.GroupValue(
		slog.String("name", "tracker"),
		slog.String("version", version),
	))
	return l
}

// todo add click to delete modal
// todo add light, moderate, and vigorous totals for past seven days
// todo add effort level color codes
// todo factor in heart rates and targets for a week (1.25-2.5h a week)
