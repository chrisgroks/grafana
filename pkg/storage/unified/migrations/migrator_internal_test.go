package migrations

import (
	"context"
	"testing"
	"time"

	authlib "github.com/grafana/authlib/types"
	"github.com/grafana/grafana/pkg/infra/log"
	"github.com/grafana/grafana/pkg/storage/unified/resource"
	"github.com/grafana/grafana/pkg/storage/unified/resourcepb"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestCollectResourceKeys(t *testing.T) {
	registry := NewMigrationRegistry()
	registry.Register(MigrationDefinition{
		ID: "test",
		Migrators: map[schema.GroupResource]MigratorFunc{
			{Group: "dashboard.grafana.app", Resource: "dashboards"}: func(_ context.Context, _ int64, _ MigrateOptions, _ resourcepb.BulkStore_BulkProcessClient) error {
				return nil
			},
			{Group: "dashboard.grafana.app", Resource: "folders"}: func(_ context.Context, _ int64, _ MigrateOptions, _ resourcepb.BulkStore_BulkProcessClient) error {
				return nil
			},
		},
	})

	keys := collectResourceKeys("stack-1", []schema.GroupResource{
		{Group: "dashboard.grafana.app", Resource: "folders"},
		{Group: "dashboard.grafana.app", Resource: "dashboards"},
		{Group: "dashboard.grafana.app", Resource: "folders"},
	}, registry)

	require.Len(t, keys, 2)
	require.Equal(t, "dashboard.grafana.app", keys[0].Group)
	require.Equal(t, "dashboards", keys[0].Resource)
	require.Equal(t, "dashboard.grafana.app", keys[1].Group)
	require.Equal(t, "folders", keys[1].Resource)
}

func TestCollectResourceKeys_DistinguishesSameResourceAcrossGroups(t *testing.T) {
	registry := NewMigrationRegistry()
	registry.Register(MigrationDefinition{
		ID: "test",
		Migrators: map[schema.GroupResource]MigratorFunc{
			{Group: "dashboard.grafana.app", Resource: "dashboards"}: func(_ context.Context, _ int64, _ MigrateOptions, _ resourcepb.BulkStore_BulkProcessClient) error {
				return nil
			},
			{Group: "example.grafana.app", Resource: "dashboards"}: func(_ context.Context, _ int64, _ MigrateOptions, _ resourcepb.BulkStore_BulkProcessClient) error {
				return nil
			},
		},
	})

	keys := collectResourceKeys("stack-1", []schema.GroupResource{
		{Group: "dashboard.grafana.app", Resource: "dashboards"},
		{Group: "example.grafana.app", Resource: "dashboards"},
	}, registry)

	require.Len(t, keys, 2)
	require.Equal(t, "dashboard.grafana.app", keys[0].Group)
	require.Equal(t, "dashboards", keys[0].Resource)
	require.Equal(t, "example.grafana.app", keys[1].Group)
	require.Equal(t, "dashboards", keys[1].Resource)
}

func TestToBuildTimeMap(t *testing.T) {
	buildTimes := []*resourcepb.RebuildIndexesResponse_IndexBuildTime{
		{
			Group:         "dashboard.grafana.app",
			Resource:      "Dashboards",
			BuildTimeUnix: 100,
		},
		{
			Group:         "dashboard.grafana.app",
			Resource:      "folders",
			BuildTimeUnix: 200,
		},
	}

	m := toBuildTimeMap(buildTimes)
	require.Equal(t, int64(100), m["dashboard.grafana.app/dashboards"])
	require.Equal(t, int64(200), m["dashboard.grafana.app/folders"])
}

func TestToBuildTimeMap_DistinguishesSameResourceAcrossGroups(t *testing.T) {
	buildTimes := []*resourcepb.RebuildIndexesResponse_IndexBuildTime{
		{
			Group:         "dashboard.grafana.app",
			Resource:      "dashboards",
			BuildTimeUnix: 100,
		},
		{
			Group:         "example.grafana.app",
			Resource:      "dashboards",
			BuildTimeUnix: 200,
		},
	}

	m := toBuildTimeMap(buildTimes)
	require.Equal(t, int64(100), m["dashboard.grafana.app/dashboards"])
	require.Equal(t, int64(200), m["example.grafana.app/dashboards"])
}

func TestRebuildIndexes_NilResponse(t *testing.T) {
	mockClient := resource.NewMockResourceClient(t)
	mockClient.EXPECT().
		RebuildIndexes(mock.Anything, mock.Anything).
		Return(nil, nil).
		Once()

	registry := NewMigrationRegistry()
	registry.Register(MigrationDefinition{
		ID: "test",
		Migrators: map[schema.GroupResource]MigratorFunc{
			{Group: "dashboard.grafana.app", Resource: "dashboards"}: func(_ context.Context, _ int64, _ MigrateOptions, _ resourcepb.BulkStore_BulkProcessClient) error {
				return nil
			},
		},
	})

	migrator := newUnifiedMigrator(nil, mockClient, log.New("test"), registry)
	err := migrator.(*unifiedMigration).rebuildIndexes(context.Background(), RebuildIndexOptions{
		NamespaceInfo: authlib.NamespaceInfo{
			OrgID: 1,
			Value: "stack-1",
		},
		Resources: []schema.GroupResource{
			{Group: "dashboard.grafana.app", Resource: "dashboards"},
		},
		MigrationFinishedAt: time.Now(),
	})
	require.NoError(t, err)
}

func TestRebuildIndexes_UsingDistributor_DistinguishesSameResourceAcrossGroups(t *testing.T) {
	mockClient := resource.NewMockResourceClient(t)
	migrationFinishedAt := time.Unix(200, 0)

	expectedKeys := map[string]struct{}{
		"alpha.grafana.app/widgets": {},
		"zeta.grafana.app/widgets":  {},
	}

	mockClient.EXPECT().
		RebuildIndexes(mock.Anything, mock.MatchedBy(func(req *resourcepb.RebuildIndexesRequest) bool {
			if req == nil || req.Namespace != "stack-1" || len(req.Keys) != 2 {
				return false
			}

			actualKeys := make(map[string]struct{}, len(req.Keys))
			for _, key := range req.Keys {
				if key == nil {
					return false
				}
				actualKeys[normalizedGroupResourceID(key.Group, key.Resource)] = struct{}{}
			}

			if len(actualKeys) != len(expectedKeys) {
				return false
			}
			for key := range expectedKeys {
				if _, ok := actualKeys[key]; !ok {
					return false
				}
			}
			return true
		})).
		Return(&resourcepb.RebuildIndexesResponse{
			ContactedAllInstances: true,
			BuildTimes: []*resourcepb.RebuildIndexesResponse_IndexBuildTime{
				{
					Group:         "alpha.grafana.app",
					Resource:      "widgets",
					BuildTimeUnix: migrationFinishedAt.Add(-1 * time.Second).Unix(),
				},
				{
					Group:         "zeta.grafana.app",
					Resource:      "widgets",
					BuildTimeUnix: migrationFinishedAt.Unix(),
				},
			},
		}, nil).
		Once()

	registry := NewMigrationRegistry()
	registry.Register(MigrationDefinition{
		ID: "test",
		Migrators: map[schema.GroupResource]MigratorFunc{
			{Group: "alpha.grafana.app", Resource: "widgets"}: func(_ context.Context, _ int64, _ MigrateOptions, _ resourcepb.BulkStore_BulkProcessClient) error {
				return nil
			},
			{Group: "zeta.grafana.app", Resource: "widgets"}: func(_ context.Context, _ int64, _ MigrateOptions, _ resourcepb.BulkStore_BulkProcessClient) error {
				return nil
			},
		},
	})

	migrator := newUnifiedMigrator(nil, mockClient, log.New("test"), registry)
	err := migrator.(*unifiedMigration).rebuildIndexes(context.Background(), RebuildIndexOptions{
		UsingDistributor: true,
		NamespaceInfo: authlib.NamespaceInfo{
			OrgID: 1,
			Value: "stack-1",
		},
		Resources: []schema.GroupResource{
			{Group: "alpha.grafana.app", Resource: "widgets"},
			{Group: "zeta.grafana.app", Resource: "widgets"},
		},
		MigrationFinishedAt: migrationFinishedAt,
	})

	require.Error(t, err)
	require.Contains(t, err.Error(), "alpha.grafana.app/widgets")
}
