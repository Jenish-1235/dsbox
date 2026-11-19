output "instance_id" {
  value = aws_instance.devbox.id
}

output "data_volume_id" {
  value = aws_ebs_volume.data.id
}

output "ssm_shell" {
  description = "Break-glass shell (no SSH or Tailscale needed)."
  value       = "aws ssm start-session --profile ${var.aws_profile} --region ${var.region} --target ${aws_instance.devbox.id}"
}

output "aws_profile" {
  value = var.aws_profile
}

output "region" {
  value = var.region
}

output "owner" {
  value = var.owner
}
