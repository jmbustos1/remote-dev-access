variable "nombre" { type = string }
variable "pipeline" { type = string }
variable "limite_usd" { type = string }
variable "email" { type = string }

# Calcado de logs-consumer (que a su vez viene de etl_contratos). Filtra por la MISMA etiqueta que ponen los default_tags
# del provider, así el presupuesto mide este proyecto y no la cuenta entera.
resource "aws_budgets_budget" "this" {
  name         = "${var.nombre}-mensual"
  budget_type  = "COST"
  limit_amount = var.limite_usd
  limit_unit   = "USD"
  time_unit    = "MONTHLY"

  cost_filter {
    name   = "TagKeyValue"
    values = ["user:Pipeline$${var.pipeline}"]
  }

  # 80 % avisa a tiempo; 100 % avisa cuando ya pasó. Sirven para cosas distintas.
  notification {
    comparison_operator        = "GREATER_THAN"
    threshold                  = 80
    threshold_type             = "PERCENTAGE"
    notification_type          = "ACTUAL"
    subscriber_email_addresses = [var.email]
  }
  notification {
    comparison_operator        = "GREATER_THAN"
    threshold                  = 100
    threshold_type             = "PERCENTAGE"
    notification_type          = "FORECASTED"
    subscriber_email_addresses = [var.email]
  }
}
