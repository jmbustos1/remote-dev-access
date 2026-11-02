# Señalización

API Gateway WebSocket + Lambda (Go, imagen en ECR) + DynamoDB. La lógica vive en
`core/signaling` y es la misma que corre `rda signal-local` en desarrollo.

## Probar sin AWS

```
./bin/rda signal-local -device pc-casa:pc:tokpc -device celular:phone:tokcel
./bin/rda-agent -url ws://127.0.0.1:8790 -device pc-casa -token tokpc
./bin/rda connect -url ws://127.0.0.1:8790 -device celular -token tokcel pc-casa
```

## Desplegar (primera vez)

La Lambda necesita que la imagen exista en ECR antes de crearse, así que el primer despliegue
va en dos pasos:

```
cd signaling/terraform && cp terraform.tfvars.example terraform.tfvars   # completar
terraform init
terraform plan                                # revisar
terraform apply -target=module.ecr            # 1) solo el repositorio de imágenes
cd ../.. && make signaling-image
#   empujar la imagen (deploy-signaling también actualiza la Lambda, que aún no existe):
ECR=$(terraform -chdir=signaling/terraform output -raw ecr_url)
aws ecr get-login-password | docker login --username AWS --password-stdin ${ECR%%/*}
docker tag rda-signaling:$(git describe --always --dirty) $ECR:latest && docker push $ECR:latest
terraform -chdir=signaling/terraform apply    # 2) el resto
terraform -chdir=signaling/terraform output siguiente_paso
```

Después: `make deploy-signaling` para cada versión nueva del código (no toca Terraform).

## Registrar dispositivos

El token se muestra una sola vez; la tabla guarda solo su SHA-256.

```
./bin/rda token
aws dynamodb put-item --table-name remote-dev-access-signaling --item \
  '{"pk":{"S":"dev#pc-casa"},"kind":{"S":"pc"},"token_hash":{"S":"<hash>"}}'
```

Revocar: `aws dynamodb delete-item --table-name remote-dev-access-signaling --key '{"pk":{"S":"dev#pc-casa"}}'`.

## Costos

Pago por uso: API Gateway WebSocket (minutos de conexión + mensajes), Lambda y DynamoDB
on-demand. Con 3 PCs conectados 24/7 y pings cada 5 min, centavos al mes. Tope de abuso:
throttling del stage (20 en ráfaga, 10/s). Presupuesto con alertas por la etiqueta
`Pipeline=signaling`.
