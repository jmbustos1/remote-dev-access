variable "nombre" { type = string }
variable "retener" { type = number }

resource "aws_ecr_repository" "this" {
  name                 = var.nombre
  image_tag_mutability = "MUTABLE"
  image_scanning_configuration { scan_on_push = true }
}

# Sin esto el repositorio acumula todas las imágenes que se hayan empujado, y se paga
# almacenamiento por cada una para siempre.
resource "aws_ecr_lifecycle_policy" "this" {
  repository = aws_ecr_repository.this.name
  policy = jsonencode({
    rules = [{
      rulePriority = 1
      description  = "Conservar solo las ultimas ${var.retener} imagenes"
      selection    = { tagStatus = "any", countType = "imageCountMoreThan", countNumber = var.retener }
      action       = { type = "expire" }
    }]
  })
}

output "url" { value = aws_ecr_repository.this.repository_url }
output "arn" { value = aws_ecr_repository.this.arn }
