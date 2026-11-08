package provider

import (
	"context"
	"slices"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/netbirdio/netbird/shared/management/http/api"
)

// Test_filterGroups checks each groups filter in isolation and combined.
func Test_filterGroups(t *testing.T) {
	jwt := api.GroupIssuedJwt
	apiIssued := api.GroupIssuedApi
	groups := []api.Group{
		{Id: "g1", Name: "All", Issued: &apiIssued},
		{Id: "g2", Name: "NetbirdUsersInfraDyn", Issued: &jwt},
		{Id: "g3", Name: "NetbirdUsersRrdDyn", Issued: &jwt},
		// A group read back without an issuer must not crash the filter.
		{Id: "g4", Name: "Legacy"},
	}

	cases := []struct {
		name     string
		filter   GroupsModel
		expected []string
	}{
		{
			name:     "no filter returns every group",
			filter:   GroupsModel{},
			expected: []string{"g1", "g2", "g3", "g4"},
		},
		{
			name:     "issued",
			filter:   GroupsModel{Issued: types.StringValue("jwt")},
			expected: []string{"g2", "g3"},
		},
		{
			name:     "name and issued are both required",
			filter:   GroupsModel{Name: types.StringValue("All"), Issued: types.StringValue("jwt")},
			expected: []string{},
		},
		{
			// The reason this data source exists: an OIDC group nobody logged in with
			// yet is an empty result, not an error.
			name:     "missing name is an empty result",
			filter:   GroupsModel{Name: types.StringValue("NetbirdUsersNotYetDyn")},
			expected: []string{},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := filterGroups(groups, c.filter)
			ids := make([]string, len(out))
			for i, g := range out {
				ids[i] = g.Id
			}
			if !slices.Equal(ids, c.expected) {
				t.Fatalf("Expected:\n%#v\nFound:\n%#v", c.expected, ids)
			}
		})
	}
}

// Test_groupsAPIToTerraform checks API groups map to the Terraform list.
func Test_groupsAPIToTerraform(t *testing.T) {
	jwt := api.GroupIssuedJwt
	groups := []api.Group{
		{Id: "g2", Name: "NetbirdUsersInfraDyn", Issued: &jwt, Peers: []api.PeerMinimum{{Id: "p1"}}},
	}

	var data GroupsModel
	if d := groupsAPIToTerraform(context.Background(), groups, &data); d.HasError() {
		t.Fatalf("Expected no error diagnostics, found %d errors", d.ErrorsCount())
	}

	var ids []string
	data.Ids.ElementsAs(context.Background(), &ids, false)
	if !slices.Equal(ids, []string{"g2"}) {
		t.Fatalf("Expected ids [g2], found %v", ids)
	}
	if len(data.Groups) != 1 || data.Groups[0].Name.ValueString() != "NetbirdUsersInfraDyn" ||
		data.Groups[0].Issued.ValueString() != "jwt" || len(data.Groups[0].Peers.Elements()) != 1 {
		t.Fatalf("Unexpected group mapping: %#v", data.Groups)
	}
}

// Test_groupsAPIToTerraform_ResourcesFitSchema checks a group holding network
// resources maps to a value the data source schema accepts, so the nested
// resources type stays in step with the singular group schema.
func Test_groupsAPIToTerraform_ResourcesFitSchema(t *testing.T) {
	ctx := context.Background()
	groups := []api.Group{
		{
			Id:        "g1",
			Name:      "Liberty Park Servers",
			Issued:    valPtr(api.GroupIssuedApi),
			Peers:     []api.PeerMinimum{{Id: "p1"}},
			Resources: []api.Resource{{Id: "r2", Type: api.ResourceTypeSubnet}, {Id: "r1", Type: api.ResourceTypeHost}},
		},
	}

	data := GroupsModel{Name: types.StringValue("Liberty Park Servers"), Issued: types.StringNull()}
	if d := groupsAPIToTerraform(ctx, groups, &data); d.HasError() {
		t.Fatalf("Expected no error diagnostics, found %v", d)
	}

	var schemaResp datasource.SchemaResponse
	(&GroupsDataSource{}).Schema(ctx, datasource.SchemaRequest{}, &schemaResp)
	state := tfsdk.State{Raw: tftypes.NewValue(schemaResp.Schema.Type().TerraformType(ctx), nil), Schema: schemaResp.Schema}
	if d := state.Set(ctx, &data); d.HasError() {
		t.Fatalf("Expected the groups data source schema to accept the mapped value, found %v", d)
	}

	var resources []groupResourceModel
	if d := data.Groups[0].Resources.ElementsAs(ctx, &resources, false); d.HasError() {
		t.Fatalf("Expected resources to decode as {id,type} objects, found %v", d)
	}
	if len(resources) != 2 || resources[0].Id.ValueString() != "r1" || resources[0].Type.ValueString() != "host" {
		t.Fatalf("Expected resources sorted by id with their types, found %#v", resources)
	}
}
