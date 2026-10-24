package api

import (
	"google.golang.org/protobuf/reflect/protoreflect"

	accessv1 "github.com/megakuul/bob/internal/gen/bob/access/v1"
	analyticsv1 "github.com/megakuul/bob/internal/gen/bob/analytics/v1"
	authv1 "github.com/megakuul/bob/internal/gen/bob/auth/v1"
	buildv1 "github.com/megakuul/bob/internal/gen/bob/build/v1"
	cachev1 "github.com/megakuul/bob/internal/gen/bob/cache/v1"
	fleetv1 "github.com/megakuul/bob/internal/gen/bob/fleet/v1"
	forgev1 "github.com/megakuul/bob/internal/gen/bob/forge/v1"
	logv1 "github.com/megakuul/bob/internal/gen/bob/log/v1"
	notifyv1 "github.com/megakuul/bob/internal/gen/bob/notify/v1"
	projectv1 "github.com/megakuul/bob/internal/gen/bob/project/v1"
	registryv1 "github.com/megakuul/bob/internal/gen/bob/registry/v1"
	releasev1 "github.com/megakuul/bob/internal/gen/bob/release/v1"
	settingsv1 "github.com/megakuul/bob/internal/gen/bob/settings/v1"
	workerv1 "github.com/megakuul/bob/internal/gen/bob/worker/v1"
)


// AllRules is the rule table of every service the API serves (the permission catalogue's
// source). It is the table New builds; New refuses to start when it is invalid.
func AllRules() (Rules, error) {
	files := []struct {
		fd   protoreflect.FileDescriptor
		svcs []protoreflect.Name
	}{
		{authv1.File_bob_auth_v1_auth_proto, []protoreflect.Name{"AuthService"}},
		{accessv1.File_bob_access_v1_access_proto, []protoreflect.Name{"UserService", "RoleService", "ServiceAccountService", "OidcService"}},
		{projectv1.File_bob_project_v1_project_proto, []protoreflect.Name{"ProjectService"}},
		{buildv1.File_bob_build_v1_build_proto, []protoreflect.Name{"BuildService", "JobService"}},
		{logv1.File_bob_log_v1_log_proto, []protoreflect.Name{"LogService"}},
		{fleetv1.File_bob_fleet_v1_fleet_proto, []protoreflect.Name{"FleetService"}},
		{cachev1.File_bob_cache_v1_cache_proto, []protoreflect.Name{"CacheService"}},
		{registryv1.File_bob_registry_v1_registry_proto, []protoreflect.Name{"RegistryService"}},
		{releasev1.File_bob_release_v1_release_proto, []protoreflect.Name{"ReleaseService"}},
		{settingsv1.File_bob_settings_v1_settings_proto, []protoreflect.Name{"SettingsService"}},
		{forgev1.File_bob_forge_v1_forge_proto, []protoreflect.Name{"ForgeService"}},
		{notifyv1.File_bob_notify_v1_notify_proto, []protoreflect.Name{"NotifyService"}},
		{analyticsv1.File_bob_analytics_v1_analytics_proto, []protoreflect.Name{"AnalyticsService"}},
		{workerv1.File_bob_worker_v1_worker_proto, []protoreflect.Name{"WorkerControlService"}},
	}
	var sds []protoreflect.ServiceDescriptor
	for _, f := range files {
		for _, n := range f.svcs {
			sd := f.fd.Services().ByName(n)
			if sd == nil {
				return nil, errMissingService(string(f.fd.Package()) + "." + string(n))
			}
			sds = append(sds, sd)
		}
	}
	return RulesOf(sds...)
}

type errMissingService string

func (e errMissingService) Error() string {
	return "api: service " + string(e) + " is missing from the generated code"
}
