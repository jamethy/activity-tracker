package main

import (
	"github.com/labstack/echo/v4"
	"log/slog"
	"time"
)

// RequestLogger is a custom version of echo.Logger which uses our structured logger
func RequestLogger() echo.MiddlewareFunc {

	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(e echo.Context) error {

			// gather some attributes for the incoming request
			bytes := e.Request().Header.Get(echo.HeaderContentLength)
			if bytes == "" {
				bytes = "0"
			}

			slog.Info("Incoming Request",
				"bytes", bytes,
			)

			// record start time to calculate duration (ms)
			startTime := time.Now()

			// execute the next middleware (controller request handler is in there somewhere)
			err := next(e)
			if err != nil {
				// call echo context Error to set response properly
				e.Error(err)
			}

			duration := time.Since(startTime)
			res := e.Response()

			// previous format: ${time_rfc3339} [INFO] [${id}] [${method} ${uri} ${status}] ${error}
			// the default context includes request id, route, method, etc
			attrs := []any{
				"status", res.Status,
				"ms", duration.Milliseconds(),
				"bytes", res.Size,
			}
			if err != nil {
				attrs = append(attrs, "err", err)
			}

			slog.Info("Request Status", attrs...)

			return nil // any err is consumed by this middleware
		}
	}
}
