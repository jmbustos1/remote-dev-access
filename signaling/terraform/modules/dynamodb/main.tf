variable "nombre" { type = string }

# Una sola tabla para dispositivos (dev#…) y conexiones (conn#…). On-demand: con un puñado
# de lecturas al día, pagar por capacidad reservada sería tirar plata.
resource "aws_dynamodb_table" "this" {
  name         = var.nombre
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "pk"

  attribute {
    name = "pk"
    type = "S"
  }

  # Las conexiones llevan "expira": si se pierde un $disconnect, DynamoDB las borra solo.
  ttl {
    attribute_name = "expira"
    enabled        = true
  }

  # Sin respaldo continuo: lo único que no se regenera solo son los dispositivos registrados,
  # y volver a registrar cuatro toma un minuto.
  point_in_time_recovery { enabled = false }
}

output "nombre" { value = aws_dynamodb_table.this.name }
output "arn" { value = aws_dynamodb_table.this.arn }
