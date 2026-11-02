output "url" {
  value       = module.apigw.url
  description = "URL de la señalización: va en RDA_URL de los agentes y en Ajustes de la APK."
}

output "ecr_url" {
  value       = module.ecr.url
  description = "Adónde empujar la imagen."
}

output "tabla" { value = module.dynamodb.nombre }
output "funcion" { value = module.lambda.nombre }

output "siguiente_paso" {
  value = <<-TXT
    1. Empujar la imagen (desde la raíz del repo):
         make deploy-signaling
    2. Registrar cada dispositivo (el token se muestra UNA vez; el servidor guarda solo el hash):
         ./bin/rda token
         aws dynamodb put-item --table-name ${module.dynamodb.nombre} --item \
           '{"pk":{"S":"dev#pc-casa"},"kind":{"S":"pc"},"token_hash":{"S":"<hash>"}}'
       Repetir con "celular" y kind "phone".
    3. En el PC:   RDA_URL=${module.apigw.url} RDA_DEVICE=pc-casa RDA_TOKEN=<token> ./bin/rda-agent
       En la APK:  Ajustes → URL ${module.apigw.url}, nombre "celular" y su token.
  TXT
}
