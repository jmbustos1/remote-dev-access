variable "aws_region" {
  type    = string
  default = "us-east-1"
}

variable "aws_profile" {
  type        = string
  default     = null
  description = "Perfil de ~/.aws/config. null = el perfil por defecto."
}

variable "proyecto" {
  type    = string
  default = "remote-dev-access"
}

variable "pipeline" {
  type        = string
  default     = "signaling"
  description = "Segunda etiqueta de costos. El relay (fase 5) usará otra."
}

variable "tag_imagen" {
  type    = string
  default = "latest"
}

variable "imagenes_a_retener" {
  type    = number
  default = 3
}

variable "dias_retencion_logs" {
  type    = number
  default = 14
}

variable "limite_rafaga" {
  type        = number
  default     = 20
  description = "Mensajes en ráfaga por segundo que acepta API Gateway. Uso real: 3 PCs y un celular."
}

variable "limite_tasa" {
  type        = number
  default     = 10
  description = "Mensajes por segundo sostenidos. Tope contra abuso: cada mensaje es una invocación."
}

variable "presupuesto_usd" {
  type        = string
  default     = "3"
  description = "Lo esperado son centavos al mes; 3 deja margen para que la alarma signifique algo."
}

variable "email_alertas" {
  type        = string
  description = "Destinatario de las alertas de presupuesto."
}
