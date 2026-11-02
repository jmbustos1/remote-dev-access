package main

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/jmbustos1/remote-dev-access/core/signaling"
)

// dynamoStore implementa signaling.Store con UNA tabla (clave "pk"):
//
//	dev#<nombre>  kind, token_hash, conn (si está conectado)
//	conn#<id>     device, expira (TTL: DynamoDB borra solas las conexiones huérfanas)
//
// Los dispositivos se registran a mano (ver signaling/README.md): la Lambda solo
// lee dev#… y actualiza su conexión.
type dynamoStore struct {
	db    *dynamodb.Client
	table string
}

// connTTL: API Gateway corta a las 2 h; un margen por si se pierde un $disconnect.
const connTTL = 3 * time.Hour

func s(v string) types.AttributeValue { return &types.AttributeValueMemberS{Value: v} }

func str(item map[string]types.AttributeValue, k string) string {
	if v, ok := item[k].(*types.AttributeValueMemberS); ok {
		return v.Value
	}
	return ""
}

func (d *dynamoStore) get(ctx context.Context, pk string) (map[string]types.AttributeValue, error) {
	out, err := d.db.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      &d.table,
		Key:            map[string]types.AttributeValue{"pk": s(pk)},
		ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return nil, err
	}
	if out.Item == nil {
		return nil, signaling.ErrNotFound
	}
	return out.Item, nil
}

func (d *dynamoStore) Device(ctx context.Context, name string) (signaling.Device, error) {
	item, err := d.get(ctx, "dev#"+name)
	if err != nil {
		return signaling.Device{}, err
	}
	return signaling.Device{Name: name, Kind: str(item, "kind"), TokenHash: str(item, "token_hash")}, nil
}

func (d *dynamoStore) Devices(ctx context.Context) ([]signaling.DeviceStatus, error) {
	// Scan está bien: son un puñado de ítems.
	out, err := d.db.Scan(ctx, &dynamodb.ScanInput{
		TableName:                 &d.table,
		FilterExpression:          aws.String("begins_with(pk, :p)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":p": s("dev#")},
	})
	if err != nil {
		return nil, err
	}
	var devs []signaling.DeviceStatus
	for _, it := range out.Items {
		devs = append(devs, signaling.DeviceStatus{
			Name:   strings.TrimPrefix(str(it, "pk"), "dev#"),
			Kind:   str(it, "kind"),
			Online: str(it, "conn") != "",
		})
	}
	return devs, nil
}

func (d *dynamoStore) SetConnection(ctx context.Context, device, connID string) error {
	expira := strconv.FormatInt(time.Now().Add(connTTL).Unix(), 10)
	if _, err := d.db.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: &d.table,
		Item: map[string]types.AttributeValue{
			"pk":     s("conn#" + connID),
			"device": s(device),
			"expira": &types.AttributeValueMemberN{Value: expira},
		},
	}); err != nil {
		return err
	}
	_, err := d.db.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:                 &d.table,
		Key:                       map[string]types.AttributeValue{"pk": s("dev#" + device)},
		UpdateExpression:          aws.String("SET conn = :c"),
		ConditionExpression:       aws.String("attribute_exists(pk)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":c": s(connID)},
	})
	return err
}

func (d *dynamoStore) ClearConnection(ctx context.Context, connID string) (string, error) {
	item, err := d.get(ctx, "conn#"+connID)
	if err != nil {
		return "", err
	}
	device := str(item, "device")
	if _, err := d.db.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: &d.table,
		Key:       map[string]types.AttributeValue{"pk": s("conn#" + connID)},
	}); err != nil {
		return "", err
	}
	// Solo si sigue siendo SU conexión: un reconectado rápido ya pudo escribir otra.
	_, err = d.db.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:                 &d.table,
		Key:                       map[string]types.AttributeValue{"pk": s("dev#" + device)},
		UpdateExpression:          aws.String("REMOVE conn"),
		ConditionExpression:       aws.String("conn = :c"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":c": s(connID)},
	})
	var cond *types.ConditionalCheckFailedException
	if errors.As(err, &cond) {
		err = nil
	}
	return device, err
}

func (d *dynamoStore) ConnectionOf(ctx context.Context, device string) (string, error) {
	item, err := d.get(ctx, "dev#"+device)
	if err != nil {
		return "", err
	}
	if c := str(item, "conn"); c != "" {
		return c, nil
	}
	return "", signaling.ErrNotFound
}

func (d *dynamoStore) DeviceOf(ctx context.Context, connID string) (string, error) {
	item, err := d.get(ctx, "conn#"+connID)
	if err != nil {
		return "", err
	}
	return str(item, "device"), nil
}
