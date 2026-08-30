package importer

import (
	"strings"
	"testing"

	"apitool/internal/core/model"
)

// insomniaV4 is a realistic Insomnia v4 export: a workspace, a NESTED folder
// (Admin inside Users), two requests (bearer + basic auth), a JSON body, and a
// Base Environment. Templates use the v4 `{{ _.name }}` tag form.
const insomniaV4 = `{
  "_type": "export",
  "__export_format": 4,
  "__export_source": "insomnia.desktop.app:v2023.5.8",
  "resources": [
    { "_id": "wrk_1", "_type": "workspace", "parentId": null, "name": "My Workspace" },
    { "_id": "fld_1", "_type": "request_group", "parentId": "wrk_1", "name": "Users" },
    { "_id": "fld_2", "_type": "request_group", "parentId": "fld_1", "name": "Admin" },
    {
      "_id": "req_1", "_type": "request", "parentId": "fld_1", "name": "List Users",
      "method": "GET", "url": "{{ _.baseUrl }}/users",
      "headers": [{ "name": "Accept", "value": "application/json" }],
      "parameters": [{ "name": "limit", "value": "5" }],
      "authentication": { "type": "bearer", "token": "{{ _.token }}" }
    },
    {
      "_id": "req_2", "_type": "request", "parentId": "fld_2", "name": "Create User",
      "method": "POST", "url": "{{ _.baseUrl }}/users",
      "body": { "mimeType": "application/json", "text": "{\"name\": \"{{ _.name }}\"}" },
      "authentication": { "type": "basic", "username": "admin", "password": "secret" }
    },
    {
      "_id": "env_1", "_type": "environment", "parentId": "wrk_1", "name": "Base Environment",
      "data": { "baseUrl": "https://api.example.com", "token": "tok_123" }
    }
  ]
}`

func TestParseInsomnia(t *testing.T) {
	res, err := ParseInsomnia([]byte(insomniaV4))
	if err != nil {
		t.Fatalf("ParseInsomnia: %v", err)
	}

	if res.WorkspaceName != "My Workspace" {
		t.Errorf("workspace name = %q", res.WorkspaceName)
	}

	// Folder tree: Users at top level, Admin nested under Users.
	var users, admin *model.Folder
	for i := range res.Folders {
		switch res.Folders[i].Name {
		case "Users":
			users = &res.Folders[i]
		case "Admin":
			admin = &res.Folders[i]
		}
	}
	if users == nil || admin == nil {
		t.Fatalf("expected Users + Admin folders, got %+v", res.Folders)
	}
	if users.ParentID != nil {
		t.Errorf("Users should be top-level (nil parent), got %v", *users.ParentID)
	}
	if admin.ParentID == nil || *admin.ParentID != users.ID {
		t.Errorf("Admin should be nested under Users")
	}

	// Requests + placement + auth kinds + template conversion.
	var list, create *model.RequestDef
	for i := range res.Requests {
		switch res.Requests[i].Name {
		case "List Users":
			list = &res.Requests[i]
		case "Create User":
			create = &res.Requests[i]
		}
	}
	if list == nil || create == nil {
		t.Fatalf("expected both requests, got %+v", res.Requests)
	}

	// {{ _.baseUrl }} -> ${baseUrl}
	if list.URL != "${baseUrl}/users" {
		t.Errorf("List URL = %q, want ${baseUrl}/users", list.URL)
	}
	if list.FolderID == nil || *list.FolderID != users.ID {
		t.Errorf("List Users should be in the Users folder")
	}
	if len(list.Params) != 1 || list.Params[0].Key != "limit" {
		t.Errorf("List params = %+v, want limit", list.Params)
	}
	if list.Auth == nil || list.Auth.Kind != model.AuthBearer || list.Auth.Bearer.Token != "${token}" {
		t.Errorf("List bearer auth wrong: %+v", list.Auth)
	}

	if create.FolderID == nil || *create.FolderID != admin.ID {
		t.Errorf("Create User should be in the Admin folder")
	}
	if create.Auth == nil || create.Auth.Kind != model.AuthBasic ||
		create.Auth.Basic.Username != "admin" || create.Auth.Basic.Password != "secret" {
		t.Errorf("Create basic auth wrong: %+v", create.Auth)
	}
	if create.Body == nil || create.Body.Kind != model.BodyJSON || !strings.Contains(create.Body.Text, "${name}") {
		t.Errorf("Create body should be JSON with ${name}, got %+v", create.Body)
	}

	// Environment reconstructed from the Base Environment's data.
	if len(res.Environments) != 1 || res.Environments[0].Name != "Base Environment" {
		t.Fatalf("expected one Base Environment, got %+v", res.Environments)
	}
	vars := map[string]string{}
	for _, v := range res.Environments[0].Variables {
		vars[v.Key] = v.Value
	}
	if vars["baseUrl"] != "https://api.example.com" || vars["token"] != "tok_123" {
		t.Errorf("env vars = %+v", vars)
	}

	assertValidOrderKeys(t, res)
}

func TestDetectInsomnia(t *testing.T) {
	if got := Detect(insomniaV4); got != FormatInsomnia {
		t.Errorf("Detect(Insomnia) = %q, want %q", got, FormatInsomnia)
	}
}

// TestInsomniaTemplateExoticLeftAsIs guards that only simple identifier tags
// are converted; nunjucks function/`{% %}` tags are preserved verbatim.
func TestInsomniaTemplateExoticLeftAsIs(t *testing.T) {
	cases := map[string]string{
		"{{ _.baseUrl }}/x":     "${baseUrl}/x",
		"{{ token }}":           "${token}",
		"{{ uuid() }}":          "{{ uuid() }}",
		"{% response 'req' %}":  "{% response 'req' %}",
		"a{{ _.a }}b{{ _.b }}c": "a${a}b${b}c",
	}
	for in, want := range cases {
		if got := convertInsomniaTemplate(in); got != want {
			t.Errorf("convertInsomniaTemplate(%q) = %q, want %q", in, got, want)
		}
	}
}
