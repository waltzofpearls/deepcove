deepcove includes the following third-party software.
{{ range . }}
--------------------------------------------------------------------------------
{{ .Name }} {{ .Version }} ({{ .LicenseName }})
{{ .LicenseURL }}

{{ .LicenseText }}
{{ end }}
