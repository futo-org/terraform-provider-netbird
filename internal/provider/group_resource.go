// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	netbird "github.com/netbirdio/netbird/shared/management/client/rest"
	"github.com/netbirdio/netbird/shared/management/http/api"
)

// Ensure provider defined types fully satisfy framework interfaces.
var _ resource.Resource = &Group{}
var _ resource.ResourceWithImportState = &Group{}

func NewGroup() resource.Resource {
	return &Group{}
}

// Group defines the resource implementation.
type Group struct {
	client *netbird.Client
}

// groupResourceObjectType is the element type for the group `resources`
// attribute. The NetBird API models each associated network resource as an
// object with an id and a type, so the Terraform attribute must mirror that
// (a list of {id, type} objects) for the value to round-trip. Declaring it as
// a list of plain strings — as the original provider did — made
// groupAPIToTerraform write string IDs while Create/Update read each element
// back as a map[string]string, producing the runtime error:
//
//	cannot reflect tftypes.String into a map, must be a map
var groupResourceObjectType = types.ObjectType{
	AttrTypes: map[string]attr.Type{
		"id":   types.StringType,
		"type": types.StringType,
	},
}

// groupResourceModel mirrors groupResourceObjectType for decoding the `resources`
// list elements back out of plan/state in Create/Update. Object elements must
// reflect into a Go struct (with tfsdk tags), NOT a map[string]string — a map
// target is only valid for a tftypes.Map, so reflecting an Object into one fails
// with "cannot reflect tftypes.Object[...] into a map, must be a map". This is the
// TF→API direction that the group-resources-roundtrip fix missed.
type groupResourceModel struct {
	Id   types.String `tfsdk:"id"`
	Type types.String `tfsdk:"type"`
}

// GroupModel describes the resource data model.
type GroupModel struct {
	Id        types.String `tfsdk:"id"`
	Name      types.String `tfsdk:"name"`
	Peers     types.List   `tfsdk:"peers"`
	Resources types.List   `tfsdk:"resources"`
	Issued    types.String `tfsdk:"issued"`
}

func (r *Group) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_group"
}

func (r *Group) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description:         "Create and assign Groups",
		MarkdownDescription: "Create and assign Groups, see [NetBird Docs](https://docs.netbird.io/how-to/manage-network-access#groups) for more information.",

		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Group ID",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "Group name identifier",
				Required:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"issued": schema.StringAttribute{
				MarkdownDescription: "Group issued by",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"peers": schema.ListAttribute{
				MarkdownDescription: "List of peers ids",
				ElementType:         types.StringType,
				Computed:            true,
				Optional:            true,
				PlanModifiers:       []planmodifier.List{listplanmodifier.UseStateForUnknown()},
				Validators:          []validator.List{listvalidator.ValueStringsAre(stringvalidator.LengthAtLeast(1))},
			},
			"resources": schema.ListAttribute{
				MarkdownDescription: "List of network resources associated with the group, each as an object with `id` and `type`. Typically populated by the API and managed from the netbird_network_resource side.",
				ElementType:         groupResourceObjectType,
				Computed:            true,
				Optional:            true,
				PlanModifiers:       []planmodifier.List{listplanmodifier.UseStateForUnknown()},
			},
		},
	}
}

func (r *Group) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	// Prevent panic if the provider has not been configured.
	if req.ProviderData == nil {
		return
	}

	client, ok := req.ProviderData.(*netbird.Client)

	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Resource Configure Type",
			fmt.Sprintf("Expected *netbird.Client, got: %T. Please report this issue to the provider developers.", req.ProviderData),
		)

		return
	}

	r.client = client
}

func groupAPIToTerraform(ctx context.Context, group *api.Group, data *GroupModel) diag.Diagnostics {
	var ret diag.Diagnostics
	data.Id = types.StringValue(group.Id)
	data.Name = types.StringValue(group.Name)
	data.Issued = types.StringPointerValue((*string)(group.Issued))
	peers := make([]string, len(group.Peers))
	for i, v := range group.Peers {
		peers[i] = v.Id
	}
	l, diag := types.ListValueFrom(ctx, types.StringType, peers)
	ret.Append(diag...)
	data.Peers = l
	resources := make([]attr.Value, len(group.Resources))
	for i, j := range group.Resources {
		o, d := types.ObjectValue(groupResourceObjectType.AttrTypes, map[string]attr.Value{
			"id":   types.StringValue(j.Id),
			"type": types.StringValue(string(j.Type)),
		})
		ret.Append(d...)
		resources[i] = o
	}
	rl, diag := types.ListValue(groupResourceObjectType, resources)
	ret.Append(diag...)
	data.Resources = rl
	return ret
}

// groupResourcesToAPI decodes the `resources` attribute (a list of {id, type}
// objects) into the API request shape, returning nil when empty so the group
// request omits the field. Object elements decode into groupResourceModel
// structs — decoding into []map[string]string (as Create/Update did before)
// fails reflection with "cannot reflect tftypes.Object[...] into a map".
func groupResourcesToAPI(ctx context.Context, list types.List) (*[]api.Resource, diag.Diagnostics) {
	var diags diag.Diagnostics
	if len(list.Elements()) == 0 {
		return nil, diags
	}
	var tfVal []groupResourceModel
	diags.Append(list.ElementsAs(ctx, &tfVal, false)...)
	if diags.HasError() {
		return nil, diags
	}
	resourcesVal := make([]api.Resource, 0, len(tfVal))
	for _, v := range tfVal {
		resourcesVal = append(resourcesVal, api.Resource{
			Id:   v.Id.ValueString(),
			Type: api.ResourceType(v.Type.ValueString()),
		})
	}
	return &resourcesVal, diags
}

func (r *Group) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data GroupModel

	// Read Terraform plan data into the model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)

	if resp.Diagnostics.HasError() {
		return
	}

	resources, resourcesDiags := groupResourcesToAPI(ctx, data.Resources)
	resp.Diagnostics.Append(resourcesDiags...)
	if resp.Diagnostics.HasError() {
		return
	}

	groupReq := api.GroupRequest{
		Name:      data.Name.ValueString(),
		Peers:     stringListDefaultPointer(ctx, data.Peers, nil),
		Resources: resources,
	}

	group, err := r.client.Groups.Create(ctx, groupReq)
	if err != nil {
		resp.Diagnostics.AddError("Error creating group", err.Error())
		return
	}

	resp.Diagnostics.Append(groupAPIToTerraform(ctx, group, &data)...)

	if resp.Diagnostics.HasError() {
		return
	}

	// Save data into Terraform state
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *Group) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data GroupModel

	// Read Terraform prior state data into the model
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)

	if resp.Diagnostics.HasError() {
		return
	}

	group, err := r.client.Groups.Get(ctx, data.Id.ValueString())
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			resp.State.RemoveResource(ctx)
			return
		} else {
			resp.Diagnostics.AddError("Error getting Group", err.Error())
		}
		return
	}

	resp.Diagnostics.Append(groupAPIToTerraform(ctx, group, &data)...)

	if resp.Diagnostics.HasError() {
		return
	}

	// Save updated data into Terraform state
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *Group) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var data GroupModel

	// Read Terraform plan data into the model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)

	if resp.Diagnostics.HasError() {
		return
	}

	if data.Id.ValueString() == "" {
		r.Create(ctx, resource.CreateRequest{Config: req.Config, Plan: req.Plan, ProviderMeta: req.Config}, (*resource.CreateResponse)(resp))
		return
	}

	resources, resourcesDiags := groupResourcesToAPI(ctx, data.Resources)
	resp.Diagnostics.Append(resourcesDiags...)
	if resp.Diagnostics.HasError() {
		return
	}

	groupReq := api.GroupRequest{
		Name:      data.Name.ValueString(),
		Peers:     stringListDefaultPointer(ctx, data.Peers, nil),
		Resources: resources,
	}

	group, err := r.client.Groups.Update(ctx, data.Id.ValueString(), groupReq)
	if err != nil {
		resp.Diagnostics.AddError("Error updating Group", err.Error())
		return
	}

	resp.Diagnostics.Append(groupAPIToTerraform(ctx, group, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Save updated data into Terraform state
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *Group) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data GroupModel

	// Read Terraform prior state data into the model
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)

	if resp.Diagnostics.HasError() {
		return
	}

	err := r.client.Groups.Delete(ctx, data.Id.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Error deleting Group", err.Error())
	}
}

func (r *Group) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
