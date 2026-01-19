module github.com/collective-projects/brm-server

go 1.25.5

require (
	github.com/collective-projects/brm-config v0.0.0
	github.com/gofrs/flock v0.13.0
	github.com/google/uuid v1.6.0
)

require (
	github.com/fsnotify/fsnotify v1.9.0 // indirect
	github.com/go-viper/mapstructure/v2 v2.4.0 // indirect
	github.com/knadh/koanf/maps v0.1.2 // indirect
	github.com/knadh/koanf/parsers/yaml v1.1.0 // indirect
	github.com/knadh/koanf/providers/env v1.0.0 // indirect
	github.com/knadh/koanf/providers/file v1.2.0 // indirect
	github.com/knadh/koanf/v2 v2.3.0 // indirect
	github.com/mitchellh/copystructure v1.2.0 // indirect
	github.com/mitchellh/reflectwalk v1.0.2 // indirect
	go.yaml.in/yaml/v3 v3.0.3 // indirect
	golang.org/x/sys v0.37.0 // indirect
)

// We should not need this line, since we are using a workspace and the brm-config module is in the workspace
replace github.com/collective-projects/brm-config => ../brm-config
