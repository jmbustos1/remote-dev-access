# Señalización de remote-dev-access: la "guía telefónica" que intercambia candidatos entre el
# celular y los PCs. Sigue el molde de logs-consumer (default_tags, presupuesto, imagen en ECR,
# código desplegado fuera de Terraform), con dos diferencias obligadas:
#
# - API Gateway WebSocket en vez de Function URL: el PC tiene que RECIBIR avisos sin preguntar
#   ("el celular quiere conectarse"), y Function URL no soporta WebSocket.
# - DynamoDB en vez de S3: hay que guardar y borrar "quién está conectado" en cada conexión,
#   con lecturas consistentes.
#
# Por las conexiones no pasa la sesión: solo mensajes de unos cientos de bytes. El tráfico real
# va directo entre celular y PC (hole punching).

terraform {
  required_version = ">= 1.5"
  required_providers {
    aws = { source = "hashicorp/aws", version = ">= 5.0" }
  }
}

provider "aws" {
  region  = var.aws_region
  profile = var.aws_profile

  default_tags {
    tags = {
      Proyecto = var.proyecto
      Pipeline = var.pipeline
      Gestion  = "terraform"
    }
  }
}

locals {
  prefijo = "${var.proyecto}-${var.pipeline}"
}

module "ecr" {
  source  = "./modules/ecr"
  nombre  = local.prefijo
  retener = var.imagenes_a_retener
}

module "dynamodb" {
  source = "./modules/dynamodb"
  nombre = local.prefijo
}

module "iam" {
  source    = "./modules/iam"
  prefijo   = local.prefijo
  tabla_arn = module.dynamodb.arn
}

module "lambda" {
  source              = "./modules/lambda"
  prefijo             = local.prefijo
  imagen              = "${module.ecr.url}:${var.tag_imagen}"
  rol_arn             = module.iam.rol_lambda_arn
  tabla               = module.dynamodb.nombre
  dias_retencion_logs = var.dias_retencion_logs
}

module "apigw" {
  source        = "./modules/apigw"
  prefijo       = local.prefijo
  lambda_invoke = module.lambda.invoke_arn
  lambda_nombre = module.lambda.nombre
  rol_nombre    = module.iam.rol_lambda_nombre
  rafaga        = var.limite_rafaga
  tasa          = var.limite_tasa
}

module "budget" {
  source     = "./modules/budget"
  nombre     = local.prefijo
  pipeline   = var.pipeline
  limite_usd = var.presupuesto_usd
  email      = var.email_alertas
}
