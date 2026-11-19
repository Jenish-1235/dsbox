# One file per devbox, kept in your private ops folder: <ops>/envs/<name>.tfvars
# (just infra-plan <name>). Never commit real values to the public repo.
region              = "eu-west-1"
aws_profile         = "devbox-alex" # an AWS CLI profile for the devbox's account
expected_account_id = "123456789012"
owner               = "alex"
alert_email         = "alex@example.com"
timezone            = "Europe/Dublin"

# Optional (defaults shown in variables.tf):
# instance_type      = "m7i.xlarge"   # 16 GiB instead of 32
# data_volume_gb     = 250
# monthly_budget_usd = 200
# snapshot_time_utc  = "02:00"
# instance_admin     = false
