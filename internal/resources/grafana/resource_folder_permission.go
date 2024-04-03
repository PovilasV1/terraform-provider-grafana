package grafana

import (
	"context"
	"strconv"

	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"

	"github.com/grafana/grafana-openapi-client-go/models"
	"github.com/grafana/terraform-provider-grafana/v2/internal/common"
)

const foldersPermissionsType = "folders"

func resourceFolderPermission() *common.Resource {
	schema := &schema.Resource{

		Description: `
Manages the entire set of permissions for a folder. Permissions that aren't specified when applying this resource will be removed.
* [Official documentation](https://grafana.com/docs/grafana/latest/administration/roles-and-permissions/access-control/)
* [HTTP API](https://grafana.com/docs/grafana/latest/developers/http_api/folder_permissions/)
`,

		CreateContext: UpdateFolderPermissions,
		ReadContext:   ReadFolderPermissions,
		UpdateContext: UpdateFolderPermissions,
		DeleteContext: DeleteFolderPermissions,
		Importer: &schema.ResourceImporter{
			StateContext: schema.ImportStatePassthroughContext,
		},

		Schema: map[string]*schema.Schema{
			"org_id": orgIDAttribute(),
			"folder_uid": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "The UID of the folder.",
			},
			"permissions": {
				Type:     schema.TypeSet,
				Optional: true,
				DefaultFunc: func() (interface{}, error) {
					return []interface{}{}, nil
				},
				Description: "The permission items to add/update. Items that are omitted from the list will be removed.",
				// Ignore the org ID of the team/SA when hashing. It works with or without it.
				Set: func(i interface{}) int {
					m := i.(map[string]interface{})
					_, teamID := SplitOrgResourceID(m["team_id"].(string))
					_, userID := SplitOrgResourceID(m["user_id"].(string))
					return schema.HashString(m["role"].(string) + teamID + userID + m["permission"].(string))
				},
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"role": {
							Type:         schema.TypeString,
							Optional:     true,
							ValidateFunc: validation.StringInSlice([]string{"Viewer", "Editor"}, false),
							Description:  "Manage permissions for `Viewer` or `Editor` roles.",
						},
						"team_id": {
							Type:        schema.TypeString,
							Optional:    true,
							Default:     "0",
							Description: "ID of the team to manage permissions for.",
						},
						"user_id": {
							Type:        schema.TypeString,
							Optional:    true,
							Default:     "0",
							Description: "ID of the user or service account to manage permissions for.",
						},
						"permission": {
							Type:         schema.TypeString,
							Required:     true,
							ValidateFunc: validation.StringInSlice([]string{"View", "Edit", "Admin"}, false),
							Description:  "Permission to associate with item. Must be one of `View`, `Edit`, or `Admin`.",
						},
					},
				},
			},
		},
	}

	return common.NewLegacySDKResource(
		"grafana_folder_permission",
		orgResourceIDString("folderUID"),
		schema,
	)
}

func UpdateFolderPermissions(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client, orgID := OAPIClientFromNewOrgResource(meta, d)

	var list []interface{}
	if v, ok := d.GetOk("permissions"); ok {
		list = v.(*schema.Set).List()
	}
	var permissionList []*models.SetResourcePermissionCommand
	for _, permission := range list {
		permission := permission.(map[string]interface{})
		permissionItem := models.SetResourcePermissionCommand{}
		if permission["role"].(string) != "" {
			permissionItem.BuiltInRole = permission["role"].(string)
		}
		_, teamIDStr := SplitOrgResourceID(permission["team_id"].(string))
		teamID, _ := strconv.ParseInt(teamIDStr, 10, 64)
		if teamID > 0 {
			permissionItem.TeamID = teamID
		}
		_, userIDStr := SplitOrgResourceID(permission["user_id"].(string))
		userID, _ := strconv.ParseInt(userIDStr, 10, 64)
		if userID > 0 {
			permissionItem.UserID = userID
		}
		permissionItem.Permission = permission["permission"].(string)
		permissionList = append(permissionList, &permissionItem)
	}

	folderUID := d.Get("folder_uid").(string)

	if err := updateResourcePermissions(client, folderUID, foldersPermissionsType, permissionList); err != nil {
		return diag.FromErr(err)
	}

	d.SetId(MakeOrgResourceID(orgID, folderUID))

	return ReadFolderPermissions(ctx, d, meta)
}

func ReadFolderPermissions(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client, orgID, folderUID := OAPIClientFromExistingOrgResource(meta, d.Id())

	// Check if the folder still exists
	_, err := client.Folders.GetFolderByUID(folderUID)
	if err, shouldReturn := common.CheckReadError("folder", d, err); shouldReturn {
		return err
	}

	resp, err := client.AccessControl.GetResourcePermissions(folderUID, foldersPermissionsType)
	if err, shouldReturn := common.CheckReadError("folder permissions", d, err); shouldReturn {
		return err
	}

	folderPermissions := resp.Payload
	var permissionItems []interface{}
	for _, permission := range folderPermissions {
		// Only managed permissions can be provisioned through this resource, so we disregard the permissions obtained through custom and fixed roles here
		if !permission.IsManaged || permission.IsInherited {
			continue
		}
		permissionItem := make(map[string]interface{})
		permissionItem["role"] = permission.BuiltInRole
		permissionItem["team_id"] = strconv.FormatInt(permission.TeamID, 10)
		permissionItem["user_id"] = strconv.FormatInt(permission.UserID, 10)
		permissionItem["permission"] = permission.Permission

		permissionItems = append(permissionItems, permissionItem)
	}

	d.SetId(MakeOrgResourceID(orgID, folderUID))
	d.Set("org_id", strconv.FormatInt(orgID, 10))
	d.Set("folder_uid", folderUID)
	d.Set("permissions", permissionItems)

	return nil
}

func DeleteFolderPermissions(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	// since permissions are tied to folders, we can't really delete the permissions.
	// we will simply remove all permissions, leaving a folder that only an admin can access.
	// if for some reason the parent folder doesn't exist, we'll just ignore the error
	client, _, folderUID := OAPIClientFromExistingOrgResource(meta, d.Id())
	err := updateResourcePermissions(client, folderUID, foldersPermissionsType, []*models.SetResourcePermissionCommand{})
	diags, _ := common.CheckReadError("folder permissions", d, err)
	return diags
}

func parsePermissionType(permission string) models.PermissionType {
	permissionInt := models.PermissionType(-1)
	switch permission {
	case "View":
		permissionInt = models.PermissionType(1)
	case "Edit":
		permissionInt = models.PermissionType(2)
	case "Admin":
		permissionInt = models.PermissionType(4)
	}
	return permissionInt
}
