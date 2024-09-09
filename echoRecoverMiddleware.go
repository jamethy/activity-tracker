package main

import (
	"fmt"
	"runtime"

	"github.com/labstack/echo/v4"
)

const stackSize = 4 << 10

// Recover custom recover middleware that prints a stacktrace better
func Recover() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			defer func() {
				if r := recover(); r != nil {

					// extract/convert the recovery information to an error
					err, ok := r.(error)
					if !ok {
						err = fmt.Errorf("%v", r)
					}

					// determine the stack and print it
					stack := make([]byte, stackSize)
					length := runtime.Stack(stack, true)
					msg := fmt.Sprintf("[PANIC RECOVER] %v %s\n", err, stack[:length])
					fmt.Println(msg)

					// send the error to the echo error handler
					c.Error(err)
				}
			}()
			return next(c)
		}
	}
}
