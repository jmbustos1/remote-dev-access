variable "prefijo" { type = string }
variable "tabla_arn" { type = string }

data "aws_iam_policy_document" "asumir" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["lambda.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "lambda" {
  name               = "${var.prefijo}-lambda"
  assume_role_policy = data.aws_iam_policy_document.asumir.json
}

# Permisos mínimos y explícitos: leer/escribir SU tabla y escribir sus logs. El permiso para
# responder por las conexiones lo agrega el módulo apigw (necesita el ARN de la API, y la API
# necesita la Lambda, que necesita este rol: ponerlo acá sería un ciclo).
data "aws_iam_policy_document" "permisos" {
  statement {
    sid       = "Tabla"
    actions   = ["dynamodb:GetItem", "dynamodb:PutItem", "dynamodb:UpdateItem", "dynamodb:DeleteItem", "dynamodb:Scan"]
    resources = [var.tabla_arn]
  }
  statement {
    sid       = "Logs"
    actions   = ["logs:CreateLogStream", "logs:PutLogEvents", "logs:CreateLogGroup"]
    resources = ["arn:aws:logs:*:*:*"]
  }
}

resource "aws_iam_role_policy" "lambda" {
  name   = "${var.prefijo}-permisos"
  role   = aws_iam_role.lambda.id
  policy = data.aws_iam_policy_document.permisos.json
}

output "rol_lambda_arn" { value = aws_iam_role.lambda.arn }
output "rol_lambda_nombre" { value = aws_iam_role.lambda.name }
