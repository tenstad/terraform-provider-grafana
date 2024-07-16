---
# generated by https://github.com/hashicorp/terraform-plugin-docs
page_title: "grafana_library_panels Data Source - terraform-provider-grafana"
subcategory: "Grafana OSS"
description: |-
  
---

# grafana_library_panels (Data Source)



## Example Usage

```terraform
resource "grafana_library_panel" "test" {
  name = "panelname"
  model_json = jsonencode({
    title       = "test name"
    type        = "text"
    version     = 0
    description = "test description"
  })
}

resource "grafana_folder" "test" {
  title = "Panel Folder"
  uid   = "panelname-folder"
}

resource "grafana_library_panel" "folder" {
  name       = "panelname In Folder"
  folder_uid = grafana_folder.test.uid
  model_json = jsonencode({
    gridPos = {
      x = 0
      y = 0
      h = 10
      w = 10
    }
    title   = "panel"
    type    = "text"
    version = 0
  })
}

data "grafana_library_panels" "all" {
  depends_on = [grafana_library_panel.folder, grafana_library_panel.test]
}
```

<!-- schema generated by tfplugindocs -->
## Schema

### Optional

- `org_id` (String) The Organization ID. If not set, the default organization is used for basic authentication, or the one that owns your service account for token authentication.

### Read-Only

- `id` (String) The ID of this resource.
- `panels` (Set of Object) (see [below for nested schema](#nestedatt--panels))

<a id="nestedatt--panels"></a>
### Nested Schema for `panels`

Read-Only:

- `description` (String)
- `folder_uid` (String)
- `model_json` (String)
- `name` (String)
- `uid` (String)