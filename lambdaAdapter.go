package main

import (
	"context"
	"github.com/aws/aws-lambda-go/events"
	"github.com/awslabs/aws-lambda-go-api-proxy/core"
	echoadapter "github.com/awslabs/aws-lambda-go-api-proxy/echo"
	"github.com/labstack/echo/v4"
	"log/slog"
	"net/http"
)

// LambdaEchoProxy is a modified copy of aws-lambda-go-api-proxy that lets me grab the request ID
// https://github.com/awslabs/aws-lambda-go-api-proxy/blob/581201ab7e19039735cc5b1cbef2e567ca2ed008/echo/adapterv2.go#L39
// This plus requestIDMiddleware gets the request ID in all the logs
func LambdaEchoProxy(e *echo.Echo) func(ctx context.Context, req events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error) {
	adapter := echoadapter.NewV2(e)
	return func(ctx context.Context, req events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error) {

		logLambdaRequest(req, func(r events.APIGatewayV2HTTPRequest) {
			slog.Info("Incoming API Gateway Request", "request", r)
		})

		httpReq, err := adapter.EventToRequestWithContext(ctx, req)
		if err != nil {
			return core.GatewayTimeoutV2(), core.NewLoggedError("Could not convert proxy event to request: %v", err)
		}
		// add in request id header
		httpReq.Header.Add(echo.HeaderXRequestID, req.RequestContext.RequestID)

		respWriter := core.NewProxyResponseWriterV2()
		adapter.Echo.ServeHTTP(http.ResponseWriter(respWriter), httpReq)

		proxyResponse, err := respWriter.GetProxyResponse()
		if err != nil {
			return core.GatewayTimeoutV2(), core.NewLoggedError("Error while generating proxy response: %v", err)
		}

		if proxyResponse.StatusCode == http.StatusInternalServerError {
			logLambdaRequest(req, func(r events.APIGatewayV2HTTPRequest) {
				slog.Error("500 Status Request", "request", r)
			})
		}

		return proxyResponse, nil
	}
}

// helper method to call the logFn function with authorization removed
func logLambdaRequest(req events.APIGatewayV2HTTPRequest, logFn func(events.APIGatewayV2HTTPRequest)) {
	authHeader, hasAuthHeader := req.Headers["authorization"]
	if hasAuthHeader {
		req.Headers["authorization"] = "********"
	}

	logFn(req)

	if hasAuthHeader {
		req.Headers["authorization"] = authHeader
	}
}
