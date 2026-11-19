# Per-devbox values go in <ops>/envs/<name>.tfvars (see envs/example.tfvars).
# The ones without a default are required.

variable "region" {
  description = "AWS region for the devbox."
  type        = string
}

variable "aws_profile" {
  description = "Local AWS CLI profile for the account the devbox lives in."
  type        = string
}

variable "expected_account_id" {
  description = "Guard rail: refuse to run against any other AWS account."
  type        = string
}

variable "owner" {
  description = "Who the box is for: the developer's Linux user name (provision.sh --user). Used in tags and resource names."
  type        = string
}

variable "alert_email" {
  description = "Where budget alerts go."
  type        = string
}

variable "hostname" {
  description = "Linux hostname; also becomes the Tailscale machine name."
  type        = string
  default     = "devbox"
}

variable "instance_type" {
  description = "Instance type. Changing it is a stop/start, no data loss. r7i.xlarge = 4 vCPU / 32 GiB, m7i.xlarge = 4 vCPU / 16 GiB."
  type        = string
  default     = "r7i.xlarge"
}

variable "availability_zone" {
  description = "AZ for the instance and its data volume (they must match). Empty means the region's \"a\" zone."
  type        = string
  default     = ""
}

variable "timezone" {
  description = "Timezone of the machine (set on first boot)."
  type        = string
  default     = "UTC"
}

variable "root_volume_gb" {
  description = "OS disk. Disposable: everything that matters lives on the data volume."
  type        = number
  default     = 50
}

variable "data_volume_gb" {
  description = "Persistent disk for /home and Docker. Can be grown online later."
  type        = number
  default     = 500
}

variable "snapshot_time_utc" {
  description = "When the daily data-volume snapshot runs, HH:MM in UTC. Pick a time the developer is usually offline."
  type        = string
  default     = "20:30"
}

variable "snapshot_retain_days" {
  description = "How many daily snapshots of the data volume to keep."
  type        = number
  default     = 7
}

variable "monthly_budget_usd" {
  description = "Budget alert threshold for the whole account."
  type        = number
  default     = 250
}

variable "instance_admin" {
  description = "Give the VM's instance role AdministratorAccess on its account, so aws on the VM (and anything running there, agents included) can manage the whole account. Off by default."
  type        = bool
  default     = false
}
