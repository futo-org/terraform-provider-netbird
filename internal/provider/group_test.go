package provider

import (
	"context"
	"reflect"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/netbirdio/netbird/shared/management/http/api"
)

func Test_groupAPIToTerraform(t *testing.T) {
	cases := []struct {
		resource *api.Group
		expected GroupModel
	}{
		{
			resource: &api.Group{
				Id:             "abc",
				Issued:         valPtr(api.GroupIssuedApi),
				Name:           "Test",
				Peers:          []api.PeerMinimum{},
				PeersCount:     0,
				Resources:      []api.Resource{},
				ResourcesCount: 0,
			},
			expected: GroupModel{
				Id:        types.StringValue("abc"),
				Issued:    types.StringValue("api"),
				Name:      types.StringValue("Test"),
				Peers:     types.ListValueMust(types.StringType, []attr.Value{}),
				Resources: types.ListValueMust(groupResourceObjectType, []attr.Value{}),
			},
		},
		{
			resource: &api.Group{
				Id:     "def",
				Issued: nil,
				Name:   "Meow",
				Peers: []api.PeerMinimum{
					{
						Id:   "c1",
						Name: "Useless",
					},
					{
						Id:   "c2",
						Name: "Also useless",
					},
				},
				PeersCount: 2,
				Resources: []api.Resource{
					{
						Id:   "r1",
						Type: api.ResourceTypeDomain,
					},
					{
						Id:   "r2",
						Type: api.ResourceTypeSubnet,
					},
				},
			},
			expected: GroupModel{
				Id:     types.StringValue("def"),
				Issued: types.StringNull(),
				Name:   types.StringValue("Meow"),
				Peers:  types.ListValueMust(types.StringType, []attr.Value{types.StringValue("c1"), types.StringValue("c2")}),
				Resources: types.ListValueMust(groupResourceObjectType, []attr.Value{
					types.ObjectValueMust(groupResourceObjectType.AttrTypes, map[string]attr.Value{
						"id":   types.StringValue("r1"),
						"type": types.StringValue(string(api.ResourceTypeDomain)),
					}),
					types.ObjectValueMust(groupResourceObjectType.AttrTypes, map[string]attr.Value{
						"id":   types.StringValue("r2"),
						"type": types.StringValue(string(api.ResourceTypeSubnet)),
					}),
				}),
			},
		},
	}

	for _, c := range cases {
		var out GroupModel
		outDiag := groupAPIToTerraform(context.Background(), c.resource, &out)
		if outDiag.HasError() {
			t.Fatalf("Expected no error diagnostics, found %d errors", outDiag.ErrorsCount())
		}

		if !reflect.DeepEqual(out, c.expected) {
			t.Fatalf("Expected:\n%#v\nFound:\n%#v", c.expected, out)
		}
	}
}

func Test_groupResourcesToAPI(t *testing.T) {
	// Empty list → nil so the group request omits the field.
	empty := types.ListValueMust(groupResourceObjectType, []attr.Value{})
	got, diags := groupResourcesToAPI(context.Background(), empty)
	if diags.HasError() {
		t.Fatalf("empty: unexpected error diagnostics: %d", diags.ErrorsCount())
	}
	if got != nil {
		t.Fatalf("empty: expected nil, got %#v", got)
	}

	// Populated list of {id, type} objects → []api.Resource. Regression: this
	// path previously decoded elements into []map[string]string and failed with
	// "cannot reflect tftypes.Object[...] into a map", breaking group Update for
	// any group that had network resources tagged into it.
	list := types.ListValueMust(groupResourceObjectType, []attr.Value{
		types.ObjectValueMust(groupResourceObjectType.AttrTypes, map[string]attr.Value{
			"id":   types.StringValue("r1"),
			"type": types.StringValue(string(api.ResourceTypeDomain)),
		}),
		types.ObjectValueMust(groupResourceObjectType.AttrTypes, map[string]attr.Value{
			"id":   types.StringValue("r2"),
			"type": types.StringValue(string(api.ResourceTypeSubnet)),
		}),
	})
	got, diags = groupResourcesToAPI(context.Background(), list)
	if diags.HasError() {
		t.Fatalf("populated: unexpected error diagnostics: %d", diags.ErrorsCount())
	}
	want := []api.Resource{
		{Id: "r1", Type: api.ResourceTypeDomain},
		{Id: "r2", Type: api.ResourceTypeSubnet},
	}
	if got == nil || !reflect.DeepEqual(*got, want) {
		t.Fatalf("populated: expected %#v, got %#v", want, got)
	}
}
