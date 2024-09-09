package main

import (
	"bytes"
	"context"
	"embed"
	"encoding/csv"
	"flag"
	"fmt"
	"github.com/a-h/templ"
	"github.com/labstack/echo/v4"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
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

// CGO_ENABLED=0 go build -o bootstrap .

// https://changelog.com/gotime/291
// https://templ.guide/
// https://github.com/a-h/templ
// https://htmx.org/
// https://rtslabs.atlassian.net/wiki/spaces/RA/pages/2571141121/Create+a+Web+App
func main() {
	useLocalFile := flag.Bool("localFile", false, "use local instead of s3")
	runLocally := flag.Bool("runLocally", false, "run locally instead of lambda")
	flag.Parse()

	slog.Info("start up config",
		"localFile", *useLocalFile,
		"runLocally", *runLocally,
	)

	getFileHandler := func(ctx context.Context) (io.ReadWriteCloser, error) {
		if *useLocalFile {
			return &LocalCSVData{
				ctx:      ctx,
				fileName: "daily-tracker-data.csv",
			}, nil
		}
		return newS3CSVData(ctx, "jamesianburns-random-data", "daily-tracker/data.csv")
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
			if err != nil || s.Value != "12345" {
				return c.Redirect(http.StatusFound, "/login")
			}
			return next(c)
		}
	})

	e.GET("", func(c echo.Context) error {
		f, err := getFileHandler(c.Request().Context())
		if err != nil {
			return err
		}
		defer safeClose(f, "get data")

		days, err := readCSV(f)
		if err != nil {
			return fmt.Errorf("reading days: %w", err)
		}

		days = fillInDates(days, time.Now())

		return render(c, page(mainContent(tracker(days))))
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

		f, err := getFileHandler(c.Request().Context())
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

	e.GET("/login", func(c echo.Context) error {
		return render(c, page(loginForm()))
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

	e.POST("/login", func(c echo.Context) error {
		var params struct {
			Username string `form:"username"`
			Password string `form:"password"`
		}
		if err := c.Bind(&params); err != nil {
			return err
		}
		if params.Username != os.Getenv("USERNAME") || params.Password != os.Getenv("PASSWORD") {
			return c.NoContent(http.StatusUnauthorized)
		}
		// https://echo.labstack.com/docs/cookies
		c.SetCookie(&http.Cookie{
			Name:  "session",
			Value: "12345",
		})
		return c.Redirect(http.StatusFound, "/")
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

//    background-color: "lightgreen";
//    height: "1em";
//    width: { fmt.Sprintf("%fem", e.DurationHours()*2) };

// https://github.com/a-h/templ/issues/789
func effortClass(e DayEntry) templ.CSSClass {

	var attributes []string
	attributes = append(attributes, "height:1em")

	var color string
	switch {
	case e.Effort >= 0.8:
		color = "#009700FF"
	case e.Effort >= 0.5:
		color = "#137813FF"
	case e.Effort >= 0.25:
		color = "#52864DFF"
	default:
		color = "#495E49FF"
	}
	attributes = append(attributes, "background-color:"+color)

	maxEmWidth := float32(10) // ?
	durationInHours := float32(e.Duration) / float32(time.Hour)
	emWidth := min(maxEmWidth, 4*durationInHours)
	attributes = append(attributes, fmt.Sprintf("width:%fem", emWidth))

	css := strings.Join(attributes, ";")

	cssID := templ.CSSID(`effort`, css)

	return templ.ComponentCSSClass{
		ID:    cssID,
		Class: templ.SafeCSS(`.` + cssID + `{` + css + `}`),
	}
}

type LocalCSVData struct {
	ctx      context.Context
	fileName string
	writer   io.WriteCloser
	reader   io.ReadCloser
}

func (l *LocalCSVData) Read(p []byte) (n int, err error) {
	if l.reader == nil {
		slog.Info("opening local csv file")
		f, err := os.Open(l.fileName)
		if err != nil {
			return 0, fmt.Errorf("failed to open file for reading: %w", err)
		}
		l.reader = f
	}
	return l.reader.Read(p)
}

func (l *LocalCSVData) Write(p []byte) (n int, err error) {
	if l.writer == nil {
		slog.Info("writing to local csv file")
		f, err := os.Create(l.fileName)
		if err != nil {
			return 0, fmt.Errorf("failed to open file for writing: %w", err)
		}
		l.writer = f
	}
	return l.writer.Write(p)
}

func (l *LocalCSVData) Close() error {
	if l.reader != nil {
		return l.reader.Close()
	}
	if l.writer != nil {
		return l.writer.Close()
	}
	return nil
}

type S3CSVData struct {
	ctx      context.Context
	s3Client *s3.Client
	bucket   string
	key      string

	writer io.ReadWriter
	reader io.ReadCloser
}

func (s *S3CSVData) Read(p []byte) (n int, err error) {
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

func (s *S3CSVData) Write(p []byte) (n int, err error) {
	if s.writer == nil {
		s.writer = &bytes.Buffer{}
	}
	return s.writer.Write(p)
}

func (s *S3CSVData) Close() error {
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
		return fmt.Errorf("failed to closer a reader: %w", err)
	}
	return nil
}

func newS3CSVData(ctx context.Context, bucket, key string) (*S3CSVData, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to load aws config: %w", err)
	}
	return &S3CSVData{
		ctx:      ctx,
		s3Client: s3.NewFromConfig(cfg),
		bucket:   bucket,
		key:      key,
	}, nil
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

func writeCSV(logs []DayLog, w io.Writer) error {
	records := toCSVRecords(logs)

	err := csv.NewWriter(w).WriteAll(records)
	if err != nil {
		return fmt.Errorf("failed to write csv: %w", err)
	}
	return nil
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
		slog.Error("failed to close closer", "name", name)
	}
}
