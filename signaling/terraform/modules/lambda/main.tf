variable "prefijo" { type = string }
variable "imagen" { type = string }
variable "rol_arn" { type = string }
variable "tabla" { type = string }
variable "dias_retencion_logs" { type = number }

resource "aws_cloudwatch_log_group" "this" {
  name              = "/aws/lambda/${var.prefijo}"
  retention_in_days = var.dias_retencion_logs
}

# Toda la señalización en una función: $connect, $disconnect y los mensajes. La lógica es la
# misma de `rda signal-local`; el binario solo cambia el almacén (DynamoDB) y el canal de
# respuesta (API de conexiones de API Gateway).
resource "aws_lambda_function" "this" {
  function_name = var.prefijo
  role          = var.rol_arn
  package_type  = "Image"
  image_uri     = var.imagen
  # Cada invocación son un par de lecturas a DynamoDB: no necesita más.
  memory_size = 128
  timeout     = 10

  environment {
    variables = {
      RDA_TABLA = var.tabla
    }
  }

  lifecycle {
    # El código se actualiza con `make deploy-signaling` (update-function-code), no con
    # terraform apply: así desplegar no exige tener el estado de terraform a mano.
    ignore_changes = [image_uri]
  }

  depends_on = [aws_cloudwatch_log_group.this]
}

output "nombre" { value = aws_lambda_function.this.function_name }
output "arn" { value = aws_lambda_function.this.arn }
output "invoke_arn" { value = aws_lambda_function.this.invoke_arn }
