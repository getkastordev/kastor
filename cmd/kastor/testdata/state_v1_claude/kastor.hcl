kastor {
  required_plugins {
    anthropic = {
      source  = "github.com/getkastordev/kastor-anthropic"
      version = "~> 0.1"
    }
  }
}

model "fast" {
  provider = "anthropic"
  id       = "claude-sonnet-4-5"
}

target "claude_agents" {
  type   = "platform"
  plugin = "anthropic"
}
