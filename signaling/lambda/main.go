// Lambda de señalización detrás de API Gateway WebSocket. Toda la lógica está
// en core/signaling (la misma que usa `rda signal-local`); acá solo se traduce
// el evento de API Gateway y se conectan DynamoDB y la API de conexiones.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/apigatewaymanagementapi"
	"github.com/aws/aws-sdk-go-v2/service/apigatewaymanagementapi/types"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"

	"github.com/jmbustos1/remote-dev-access/core/logx"
	"github.com/jmbustos1/remote-dev-access/core/signaling"
)

// poster responde por la API de conexiones de API Gateway. El endpoint
// (https://<dominio>/<stage>) se conoce recién con el primer evento.
type poster struct {
	cfg    aws.Config
	once   sync.Once
	client *apigatewaymanagementapi.Client
}

func (p *poster) init(domain, stage string) {
	p.once.Do(func() {
		endpoint := "https://" + domain + "/" + stage
		p.client = apigatewaymanagementapi.NewFromConfig(p.cfg, func(o *apigatewaymanagementapi.Options) {
			o.BaseEndpoint = aws.String(endpoint)
		})
	})
}

func (p *poster) Post(ctx context.Context, connID string, msg []byte) error {
	_, err := p.client.PostToConnection(ctx, &apigatewaymanagementapi.PostToConnectionInput{
		ConnectionId: aws.String(connID),
		Data:         msg,
	})
	var gone *types.GoneException
	if errors.As(err, &gone) {
		return signaling.ErrGone
	}
	return err
}

// header busca sin importar mayúsculas (API Gateway no garantiza el formato).
func header(h map[string]string, name string) string {
	for k, v := range h {
		if strings.EqualFold(k, name) {
			return v
		}
	}
	return ""
}

func main() {
	logx.Setup(os.Stderr, os.Getenv("RDA_DEBUG") == "1")
	log := logx.For(logx.StageSignaling)

	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		slog.Error("config AWS", "err", err)
		os.Exit(1)
	}
	p := &poster{cfg: cfg}
	hub := &signaling.Hub{
		Store:  &dynamoStore{db: dynamodb.NewFromConfig(cfg), table: os.Getenv("RDA_TABLA")},
		Poster: p,
	}

	lambda.Start(func(ctx context.Context, req events.APIGatewayWebsocketProxyRequest) (events.APIGatewayProxyResponse, error) {
		rc := req.RequestContext
		p.init(rc.DomainName, rc.Stage)
		ok := events.APIGatewayProxyResponse{StatusCode: http.StatusOK}

		switch rc.RouteKey {
		case "$connect":
			device := header(req.Headers, signaling.HeaderDevice)
			token := strings.TrimPrefix(header(req.Headers, signaling.HeaderAuth), "Bearer ")
			if err := hub.Connect(ctx, rc.ConnectionID, device, token); err != nil {
				// Nunca loguear el token.
				log.Warn("conexión rechazada", "device", device, "ip", rc.Identity.SourceIP)
				return events.APIGatewayProxyResponse{StatusCode: http.StatusUnauthorized}, nil
			}
			log.Info("dispositivo conectado", "device", device, "conn", rc.ConnectionID)
		case "$disconnect":
			if err := hub.Disconnect(ctx, rc.ConnectionID); err != nil {
				log.Warn("error al desconectar", "conn", rc.ConnectionID, "err", err)
			}
		default:
			if err := hub.Message(ctx, rc.ConnectionID, []byte(req.Body)); err != nil {
				log.Warn("mensaje con error", "conn", rc.ConnectionID, "err", err)
				if errors.Is(err, signaling.ErrUnauthorized) {
					return events.APIGatewayProxyResponse{StatusCode: http.StatusForbidden}, nil
				}
			}
		}
		return ok, nil
	})
}
