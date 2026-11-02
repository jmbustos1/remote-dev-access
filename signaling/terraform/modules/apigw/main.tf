variable "prefijo" { type = string }
variable "lambda_invoke" { type = string }
variable "rol_nombre" { type = string }
variable "lambda_nombre" { type = string }
variable "rafaga" { type = number }
variable "tasa" { type = number }

# API WebSocket: mantiene una conexión abierta con cada dispositivo y llama a la Lambda por
# cada evento. Tres rutas bastan: la Lambda decide por el campo "action" del mensaje.
resource "aws_apigatewayv2_api" "this" {
  name                       = var.prefijo
  protocol_type              = "WEBSOCKET"
  route_selection_expression = "$request.body.action"
}

resource "aws_apigatewayv2_integration" "lambda" {
  api_id             = aws_apigatewayv2_api.this.id
  integration_type   = "AWS_PROXY"
  integration_uri    = var.lambda_invoke
  integration_method = "POST"
}

resource "aws_apigatewayv2_route" "rutas" {
  for_each  = toset(["$connect", "$disconnect", "$default"])
  api_id    = aws_apigatewayv2_api.this.id
  route_key = each.value
  target    = "integrations/${aws_apigatewayv2_integration.lambda.id}"
}

resource "aws_apigatewayv2_stage" "prod" {
  api_id      = aws_apigatewayv2_api.this.id
  name        = "prod"
  auto_deploy = true

  # Tope de costos: cada mensaje es una invocación. Holgado para 3 PCs y un celular, y corta
  # en seco a cualquiera que intente inundar la API.
  default_route_settings {
    throttling_burst_limit = var.rafaga
    throttling_rate_limit  = var.tasa
  }
}

resource "aws_lambda_permission" "apigw" {
  statement_id  = "AllowAPIGatewayInvoke"
  action        = "lambda:InvokeFunction"
  function_name = var.lambda_nombre
  principal     = "apigateway.amazonaws.com"
  source_arn    = "${aws_apigatewayv2_api.this.execution_arn}/*/*"
}

# La Lambda responde a cada dispositivo por la API de conexiones de ESTA API, y nada más.
resource "aws_iam_role_policy" "conexiones" {
  name = "${var.prefijo}-conexiones"
  role = var.rol_nombre
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Allow"
      Action   = "execute-api:ManageConnections"
      Resource = "${aws_apigatewayv2_api.this.execution_arn}/*"
    }]
  })
}

output "url" { value = aws_apigatewayv2_stage.prod.invoke_url }
output "execution_arn" { value = aws_apigatewayv2_api.this.execution_arn }
