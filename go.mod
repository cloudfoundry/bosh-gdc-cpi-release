module github.com/cloudfoundry/bosh-gdc-cpi-release

// `version` and `sdks` for `go_download_sdk` needs to be synced with the Go minor version here
// LINT.IfChange
go 1.26.10

// LINT.ThenChange(/build/external/go_sdk.bzl:go_sdk)

require (
	github.com/aws/aws-sdk-go v1.55.5
	github.com/fsnotify/fsnotify v1.9.0 // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/gogo/protobuf v1.3.2 // indirect
	github.com/google/go-cmp v0.7.0
	github.com/google/tink/go v1.7.1-0.20221117221331-86d32cd19d06
	github.com/google/uuid v1.6.0
	github.com/imdario/mergo v0.3.16 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/prometheus/client_golang v1.22.0 // indirect
	github.com/prometheus/common v0.65.0 // indirect
	github.com/spf13/pflag v1.0.7 // indirect
	golang.org/x/crypto v0.58.0 // indirect
	golang.org/x/net v0.62.0 // indirect
	golang.org/x/oauth2 v0.37.0
	golang.org/x/term v0.47.0 // indirect
	gomodules.xyz/jsonpatch/v2 v2.5.0 // indirect
	google.golang.org/protobuf v1.36.12
	gopkg.in/inf.v0 v0.9.1 // indirect
	gopkg.in/yaml.v2 v2.4.0 // indirect
	gopkg.in/yaml.v3 v3.0.1
	k8s.io/api v0.33.3
	k8s.io/apiextensions-apiserver v0.33.3 // indirect
	k8s.io/apimachinery v0.33.3
	k8s.io/client-go v12.0.0+incompatible
	k8s.io/klog/v2 v2.130.1 // indirect
	k8s.io/kube-openapi v0.29.0 // indirect
	k8s.io/utils v0.0.0-20250604170112-4c0f3b243397
	sigs.k8s.io/controller-runtime v0.21.0
	sigs.k8s.io/yaml v1.5.0 // indirect
)

require (
	github.com/cloudfoundry/bosh-cpi-go v0.0.0-20260221100037-8b66e1def6f1
	github.com/cloudfoundry/bosh-utils v0.0.667
	github.com/googlecloudplatform/google-distributed-cloud-apis v0.0.0-20260824201624-0c18fa0ddc04
	github.com/hashicorp/go-multierror v1.1.1
	golang.org/x/exp v0.0.0-20250718183923-645b1fa84792
	sigs.k8s.io/cluster-api v1.10.2
)

require (
	cel.dev/expr v0.25.1 // indirect
	github.com/antlr4-go/antlr/v4 v4.13.1 // indirect
	github.com/bmatcuk/doublestar v1.3.4 // indirect
	github.com/emicklei/go-restful/v3 v3.12.2 // indirect
	github.com/fxamacker/cbor/v2 v2.8.0 // indirect
	github.com/google/gnostic-models v0.7.0 // indirect
	github.com/nu7hatch/gouuid v0.0.0-20131221200532-179d4d0c4d8d // indirect
	github.com/rogpeppe/go-internal v1.13.1 // indirect
	github.com/x448/float16 v0.8.4 // indirect
	go.yaml.in/yaml/v2 v2.4.2 // indirect
	gopkg.in/evanphx/json-patch.v4 v4.12.0 // indirect
)

require (
	github.com/google/cel-go v0.25.0 // indirect
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.27.1 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace v1.36.0 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20260414002931-afd174a4e478 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260414002931-afd174a4e478 // indirect
	google.golang.org/grpc v1.82.1 // indirect
	sigs.k8s.io/json v0.0.0-20241014173422-cfa47c3a1cc8 // indirect
)

require (
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/blang/semver/v4 v4.0.0 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/evanphx/json-patch/v5 v5.9.11 // indirect
	github.com/go-openapi/jsonpointer v0.21.1 // indirect
	github.com/go-openapi/jsonreference v0.21.0 // indirect
	github.com/go-openapi/swag v0.23.1 // indirect
	github.com/gobuffalo/flect v1.0.3 // indirect
	github.com/golang/groupcache v0.0.0-20241129210726-2c02b8208cf8 // indirect
	github.com/golang/protobuf v1.5.4 // indirect
	github.com/google/gofuzz v1.2.1-0.20210504230335-f78f29fc09ea // indirect
	github.com/hashicorp/errwrap v1.1.0 // indirect
	github.com/jmespath/go-jmespath v0.4.1-0.20220621161143-b0104c826a24 // indirect
	github.com/josharian/intern v1.0.0 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/mailru/easyjson v0.9.0 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.2 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/onsi/gomega v1.43.1 // indirect
	github.com/prometheus/client_model v0.6.2 // indirect
	github.com/prometheus/procfs v0.16.1 // indirect
	go.opentelemetry.io/otel/sdk v1.43.0 // indirect
	go.opentelemetry.io/otel/trace v1.43.0 // indirect
	go.opentelemetry.io/proto/otlp v1.7.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/text v0.43.0 // indirect
	golang.org/x/time v0.12.0 // indirect
	k8s.io/cluster-bootstrap v0.32.4 // indirect
	sigs.k8s.io/structured-merge-diff/v4 v4.7.0 // indirect
)

// Keep in sync with ABM
replace (
	k8s.io/api => k8s.io/api v0.31.0
	k8s.io/apiextensions-apiserver => k8s.io/apiextensions-apiserver v0.31.0
	k8s.io/apimachinery => k8s.io/apimachinery v0.31.0
	k8s.io/apiserver => k8s.io/apiserver v0.31.0
	k8s.io/client-go => k8s.io/client-go v0.31.0
	k8s.io/cloud-provider => k8s.io/cloud-provider v0.29.0
	k8s.io/cluster-bootstrap => k8s.io/cluster-bootstrap v0.29.0
	k8s.io/code-generator => k8s.io/code-generator v0.29.0
	k8s.io/component-base => k8s.io/component-base v0.31.0
	k8s.io/component-helpers => k8s.io/component-helpers v0.29.0
	k8s.io/controller-manager => k8s.io/controller-manager v0.29.0
	k8s.io/kms => k8s.io/kms v0.29.0
	k8s.io/kube-aggregator => k8s.io/kube-aggregator v0.29.0
	k8s.io/kube-openapi => k8s.io/kube-openapi v0.0.0-20240228011516-70dd3763d340
	k8s.io/kubelet => k8s.io/kubelet v0.29.0
	k8s.io/metrics => k8s.io/metrics v0.29.0
	sigs.k8s.io/controller-runtime => sigs.k8s.io/controller-runtime v0.19.0
)

// TODO(b/293290170)
replace go.etcd.io/etcd/client/pkg/v3 => go.etcd.io/etcd/client/pkg/v3 v3.5.4

replace go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc => go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc v0.53.0

// TODO(b/294931034) Pin to avoid controller-tools generating wrong CRD for celldns.
replace github.com/gobuffalo/flect => github.com/gobuffalo/flect v0.3.0

// Needed to update oc/obj/pkg/s3/client_mock.go as it uses aws-sdk-go v1.45.7
replace github.com/aws/aws-sdk-go => github.com/aws/aws-sdk-go v1.45.7

replace (
	github.com/google/gnostic-models => github.com/google/gnostic-models v0.6.9
	sigs.k8s.io/controller-tools => sigs.k8s.io/controller-tools v0.17.3
)
