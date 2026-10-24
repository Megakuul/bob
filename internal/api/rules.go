package api

import (
	"fmt"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	authzv1 "github.com/megakuul/bob/internal/gen/bob/authz/v1"
)

// Rule is the authorization rule of one RPC (option bob.authz.v1.rule, SPEC §6.5).
type Rule struct {
	Method       string // full method name "bob.build.v1.BuildService/GetBuild"
	Kind         authzv1.Kind
	ProjectField []protoreflect.Name // KIND_PROJECT: path to a string field of the request
	Resolve      authzv1.Resolve
	Download     bool // KIND_AUTHENTICATED: bob_dt_ tokens are accepted too
	Stream       bool // client or server streaming
}

// KindName is the rule kind without its prefix ("PROJECT"), as ListPermissionCatalog shows it.
func (r *Rule) KindName() string { return strings.TrimPrefix(r.Kind.String(), "KIND_") }

// ruleOf reads and validates the rule of one method. A method without a rule, with an
// unknown kind or with a PROJECT rule that names no string field of the request is an error:
// the server refuses to serve it (R-AUTH-006).
func ruleOf(md protoreflect.MethodDescriptor) (*Rule, error) {
	svc := md.Parent().(protoreflect.ServiceDescriptor)
	r := &Rule{
		Method: string(svc.FullName()) + "/" + string(md.Name()),
		Stream: md.IsStreamingClient() || md.IsStreamingServer(),
	}
	opts := md.Options()
	if opts == nil || !proto.HasExtension(opts, authzv1.E_Rule) {
		return nil, fmt.Errorf("api: %s has no bob.authz.v1.rule", r.Method)
	}
	pr, ok := proto.GetExtension(opts, authzv1.E_Rule).(*authzv1.Rule)
	if !ok || pr == nil {
		return nil, fmt.Errorf("api: %s has no bob.authz.v1.rule", r.Method)
	}
	r.Kind, r.Resolve, r.Download = pr.GetKind(), pr.GetResolve(), pr.GetAllowDownloadScope()
	isWorker := svc.ParentFile().Package() == "bob.worker.v1"
	switch r.Kind {
	case authzv1.Kind_KIND_GLOBAL, authzv1.Kind_KIND_LIST_FILTERED, authzv1.Kind_KIND_AUTHENTICATED, authzv1.Kind_KIND_PUBLIC:
	case authzv1.Kind_KIND_WORKER:
		if !isWorker {
			return nil, fmt.Errorf("api: %s: KIND_WORKER outside bob.worker.v1", r.Method)
		}
	case authzv1.Kind_KIND_PROJECT:
		path, err := fieldPath(md.Input(), pr.GetProjectField())
		if err != nil {
			return nil, fmt.Errorf("api: %s: project_field: %w", r.Method, err)
		}
		r.ProjectField = path
	default:
		return nil, fmt.Errorf("api: %s: rule kind %s is not servable", r.Method, r.Kind)
	}
	if isWorker && r.Kind != authzv1.Kind_KIND_WORKER {
		return nil, fmt.Errorf("api: %s: bob.worker.v1 methods must be KIND_WORKER", r.Method)
	}
	if r.Download && r.Kind != authzv1.Kind_KIND_AUTHENTICATED {
		return nil, fmt.Errorf("api: %s: allow_download_scope needs KIND_AUTHENTICATED", r.Method)
	}
	return r, nil
}

// fieldPath resolves a dotted path ("project.id") to a singular string field of msg.
func fieldPath(msg protoreflect.MessageDescriptor, dotted string) ([]protoreflect.Name, error) {
	if dotted == "" {
		return nil, fmt.Errorf("empty")
	}
	var out []protoreflect.Name
	parts := strings.Split(dotted, ".")
	for i, p := range parts {
		fd := msg.Fields().ByName(protoreflect.Name(p))
		if fd == nil || fd.IsList() || fd.IsMap() {
			return nil, fmt.Errorf("%q: %s has no singular field %q", dotted, msg.FullName(), p)
		}
		out = append(out, fd.Name())
		if i == len(parts)-1 {
			if fd.Kind() != protoreflect.StringKind {
				return nil, fmt.Errorf("%q: %s is not a string", dotted, fd.FullName())
			}
			return out, nil
		}
		if fd.Kind() != protoreflect.MessageKind {
			return nil, fmt.Errorf("%q: %s is not a message", dotted, fd.FullName())
		}
		msg = fd.Message()
	}
	return out, nil
}

// projectOf reads the rule's project field from a request ("" when unset).
func (r *Rule) projectOf(req any) string {
	m, ok := req.(proto.Message)
	if !ok || len(r.ProjectField) == 0 {
		return ""
	}
	cur := m.ProtoReflect()
	for i, name := range r.ProjectField {
		fd := cur.Descriptor().Fields().ByName(name)
		if fd == nil {
			return ""
		}
		if i == len(r.ProjectField)-1 {
			return cur.Get(fd).String()
		}
		if !cur.Has(fd) {
			return ""
		}
		cur = cur.Get(fd).Message()
	}
	return ""
}

// Rules is the rule table of a set of services, keyed by connect procedure
// ("/bob.auth.v1.AuthService/WhoAmI").
type Rules map[string]*Rule

// RulesOf builds the rule table of the given services. Any method without a valid rule is an
// error.
func RulesOf(services ...protoreflect.ServiceDescriptor) (Rules, error) {
	rs := Rules{}
	for _, sd := range services {
		methods := sd.Methods()
		for i := range methods.Len() {
			r, err := ruleOf(methods.Get(i))
			if err != nil {
				return nil, err
			}
			rs["/"+r.Method] = r
		}
	}
	return rs, nil
}
