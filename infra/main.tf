locals {
  availability_zone = var.availability_zone != "" ? var.availability_zone : "${var.region}a"
}

data "aws_caller_identity" "current" {
  lifecycle {
    postcondition {
      condition     = self.account_id == var.expected_account_id
      error_message = "Wrong AWS account (${self.account_id}); expected ${var.expected_account_id}. Check the aws_profile."
    }
  }
}

# --- Network: default VPC, no inbound access except Tailscale's WireGuard port ---

data "aws_vpc" "default" {
  default = true
}

data "aws_subnet" "default_az" {
  vpc_id            = data.aws_vpc.default.id
  availability_zone = local.availability_zone
  default_for_az    = true
}

resource "aws_security_group" "devbox" {
  name        = "${var.hostname}-${var.owner}"
  description = "Devbox: no inbound except Tailscale direct connections"
  vpc_id      = data.aws_vpc.default.id

  # WireGuard silently drops unauthenticated packets, so this does not expose a service.
  # It lets Tailscale make direct (not relayed) connections, which means lower latency.
  ingress {
    description = "Tailscale WireGuard"
    from_port   = 41641
    to_port     = 41641
    protocol    = "udp"
    cidr_blocks = ["0.0.0.0/0"]
  }

  egress {
    description = "All outbound"
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }
}

# --- Instance role: SSM (break-glass shell, no SSH needed) ---

resource "aws_iam_role" "devbox" {
  name = "${var.hostname}-${var.owner}-instance"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "ec2.amazonaws.com" }
      Action    = "sts:AssumeRole"
    }]
  })
}

resource "aws_iam_role_policy_attachment" "ssm" {
  role       = aws_iam_role.devbox.name
  policy_arn = "arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore"
}

# Optional: full admin on the account for the VM (var.instance_admin). Docker
# containers can't reach the role (IMDS hop limit 1).
resource "aws_iam_role_policy_attachment" "admin" {
  count      = var.instance_admin ? 1 : 0
  role       = aws_iam_role.devbox.name
  policy_arn = "arn:aws:iam::aws:policy/AdministratorAccess"
}

resource "aws_iam_instance_profile" "devbox" {
  name = aws_iam_role.devbox.name
  role = aws_iam_role.devbox.name
}

# --- Persistent data volume: /home and /var/lib/docker live here ---

resource "aws_ebs_volume" "data" {
  availability_zone = local.availability_zone
  size              = var.data_volume_gb
  type              = "gp3"
  encrypted         = true

  tags = {
    Name   = "${var.hostname}-${var.owner}-data"
    Backup = "daily"
  }

  lifecycle {
    prevent_destroy = true
  }
}

# --- Instance ---

data "aws_ssm_parameter" "ubuntu_ami" {
  name = "/aws/service/canonical/ubuntu/server/24.04/stable/current/amd64/hvm/ebs-gp3/ami-id"
}

resource "aws_instance" "devbox" {
  ami                    = data.aws_ssm_parameter.ubuntu_ami.value
  instance_type          = var.instance_type
  subnet_id              = data.aws_subnet.default_az.id
  vpc_security_group_ids = [aws_security_group.devbox.id]
  iam_instance_profile   = aws_iam_instance_profile.devbox.name

  associate_public_ip_address = true # outbound internet without a NAT gateway; no inbound allowed
  disable_api_termination     = true

  metadata_options {
    http_tokens                 = "required"
    http_put_response_hop_limit = 1 # containers cannot reach the instance role credentials
  }

  root_block_device {
    volume_size           = var.root_volume_gb
    volume_type           = "gp3"
    encrypted             = true
    delete_on_termination = true
    tags                  = { Name = "${var.hostname}-${var.owner}-root" }
  }

  user_data = templatefile("${path.module}/cloud-init.yaml.tftpl", {
    hostname       = var.hostname
    timezone       = var.timezone
    data_volume_id = replace(aws_ebs_volume.data.id, "-", "")
  })

  tags = {
    Name = "${var.hostname}-${var.owner}"
  }

  lifecycle {
    # New Ubuntu AMIs and user_data tweaks must never silently replace or reboot the machine.
    ignore_changes = [ami, user_data]
  }
}

resource "aws_volume_attachment" "data" {
  device_name = "/dev/sdf"
  volume_id   = aws_ebs_volume.data.id
  instance_id = aws_instance.devbox.id
}

# --- Daily snapshots of the data volume ---

resource "aws_iam_role" "dlm" {
  name = "${var.hostname}-${var.owner}-dlm"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "dlm.amazonaws.com" }
      Action    = "sts:AssumeRole"
    }]
  })
}

resource "aws_iam_role_policy_attachment" "dlm" {
  role       = aws_iam_role.dlm.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AWSDataLifecycleManagerServiceRole"
}

resource "aws_dlm_lifecycle_policy" "data_daily" {
  description        = "Daily snapshots of devbox data volume"
  execution_role_arn = aws_iam_role.dlm.arn
  state              = "ENABLED"

  policy_details {
    resource_types = ["VOLUME"]
    target_tags    = { Backup = "daily" }

    schedule {
      name      = "daily"
      copy_tags = true

      create_rule {
        interval      = 24
        interval_unit = "HOURS"
        times         = [var.snapshot_time_utc]
      }

      retain_rule {
        count = var.snapshot_retain_days
      }
    }
  }
}

# --- Budget alert ---

resource "aws_budgets_budget" "monthly" {
  name         = "${var.hostname}-${var.owner}-monthly"
  budget_type  = "COST"
  limit_amount = tostring(var.monthly_budget_usd)
  limit_unit   = "USD"
  time_unit    = "MONTHLY"

  notification {
    comparison_operator        = "GREATER_THAN"
    threshold                  = 80
    threshold_type             = "PERCENTAGE"
    notification_type          = "ACTUAL"
    subscriber_email_addresses = [var.alert_email]
  }

  notification {
    comparison_operator        = "GREATER_THAN"
    threshold                  = 100
    threshold_type             = "PERCENTAGE"
    notification_type          = "FORECASTED"
    subscriber_email_addresses = [var.alert_email]
  }
}
