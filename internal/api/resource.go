package api

import (
	"context"
	"net/http"
)

// FieldType is the storage/serialization type of a resource field.
type FieldType int

const (
	TString FieldType = iota
	TText
	TInt
	TFloat
	TBool
	TJSON
	TFK
	TChoice
	TDateTime
	// TSecret fields are encrypted at rest and returned as "$encrypted$".
	TSecret
)

// metadataType is the type name used in OPTIONS responses, matching AWX.
func (t FieldType) metadataType() string {
	switch t {
	case TString, TText, TSecret:
		return "string"
	case TInt:
		return "integer"
	case TFloat:
		return "float"
	case TBool:
		return "boolean"
	case TJSON:
		return "json"
	case TFK:
		return "id"
	case TChoice:
		return "choice"
	case TDateTime:
		return "datetime"
	}
	return "string"
}

// Choice is a (value, label) pair.
type Choice struct {
	Value any
	Label string
}

// Field describes a single attribute of a resource.
type Field struct {
	Name string
	// Column defaults to Name, or Name+"_id" for foreign keys.
	Column string
	Type   FieldType
	// Ref is the resource name a TFK field points to.
	Ref       string
	Required  bool
	ReadOnly  bool
	WriteOnly bool
	// CreateOnly fields can be set on POST but not changed afterwards.
	CreateOnly bool
	Nullable   bool
	Default    any
	MaxLength  int
	Choices    []Choice
	Label      string
	Help       string
	// NoFilter disables filtering and ordering on the field.
	NoFilter bool
	// Hidden fields are filterable/writable but not serialized.
	Hidden bool
	// Validate is called with the coerced value on write.
	Validate func(c *Ctx, v any, data Obj) error
	// Virtual fields are accepted on write and passed to hooks but not stored.
	Virtual bool
	// Redact masks the value in the activity stream and history views.
	Redact func(v any) any
}

func (f *Field) column() string {
	if f.Column != "" {
		return f.Column
	}
	if f.Type == TFK {
		return f.Name + "_id"
	}
	return f.Name
}

// Obj is a serialized or partially serialized object.
type Obj = map[string]any

// SubList is a related collection exposed under /<resource>/<id>/<name>/.
type SubList struct {
	Name   string
	Target string
	// Filter returns a SQL predicate on alias t restricting Target rows to those related to parent.
	Filter func(arg ArgFunc, parentID int64) string
	// ParentField: POSTing a new object to the sublist sets this field to the parent id.
	ParentField string
	// Associate/Disassociate enable POST {"id": N} and {"id": N, "disassociate": true}.
	Associate    func(c *Ctx, parentID, childID int64) error
	Disassociate func(c *Ctx, parentID, childID int64) error
	// CanAttach is checked before associate/disassociate in addition to change on the parent.
	CanAttach func(c *Ctx, parent Obj, childID int64) error
	// NoCreate disables creation of new objects through the sublist.
	NoCreate bool
	// FindExisting maps a creation request to an existing object to associate
	// instead (e.g. labels by name).
	FindExisting func(c *Ctx, parent Obj, data Obj) (int64, bool, error)
	// CreateDefaults supplies field values for objects created through the sublist
	// (e.g. a host created under a group inherits the group's inventory).
	CreateDefaults func(c *Ctx, parent Obj) Obj
	// ReadOnly disables all writes through the sublist.
	ReadOnly bool
	// SkipView lists targets without their own visibility filter (access follows the parent).
	SkipView bool
	// AttachPerm is the permission required on the parent to (dis)associate. Defaults to "change".
	AttachPerm string
}

// Action is a custom endpoint under /<resource>/<id>/<name>/.
type Action struct {
	Name    string
	Methods []string
	Handler func(c *Ctx, w http.ResponseWriter, r *http.Request, obj Obj)
	// Perm required on the object, defaults to "view" for GET and "change" otherwise.
	Perm string
}

// ArgFunc appends a query argument and returns its placeholder.
type ArgFunc func(v any) string

// Resource describes an API collection backed by a table.
type Resource struct {
	// Name is the URL segment, e.g. "job_templates".
	Name string
	// Type is the AWX object type, e.g. "job_template".
	Type string
	// Table backing the resource.
	Table string
	// Discriminator restricts rows to type = Type (polymorphic tables).
	Discriminator bool
	// Plural/Singular display names for OPTIONS.
	Verbose string
	Fields  []*Field
	// NoTimestamps disables created/modified/created_by/modified_by.
	NoTimestamps bool
	// NoName: resource has no name/description columns.
	NoName       bool
	SearchFields []string
	Ordering     string

	// RBAC: ContentType is the RBAC content type; OrgColumn the column holding the organization id.
	ContentType string
	OrgColumn   string
	// FolderColumn holds the folder id; assignments on that folder and its ancestors apply.
	FolderColumn string
	// View returns a SQL predicate restricting visible rows (alias t). Overrides the default.
	View func(c *Ctx, arg ArgFunc) string
	// CanAdd validates permission to create the object.
	CanAdd func(c *Ctx, data Obj) error
	// CanChange validates permission to modify an existing object; data holds the merged new values.
	CanChange func(c *Ctx, obj Obj, data Obj) error
	// CanDelete validates permission to delete.
	CanDelete func(c *Ctx, obj Obj) error

	// Hooks.
	BeforeSave   func(c *Ctx, old Obj, data Obj) error
	AfterCreate  func(c *Ctx, obj Obj) error
	AfterUpdate  func(c *Ctx, old, obj Obj) error
	BeforeDelete func(c *Ctx, obj Obj) error
	AfterDelete  func(c *Ctx, obj Obj) error

	// Related adds custom related links.
	Related func(c *Ctx, obj Obj, rel map[string]any)
	// Summary adds custom summary fields for a page of objects.
	Summary func(c *Ctx, objs []Obj) error
	// SummaryFields are the columns included when this resource is referenced from another (default id,name,description).
	SummaryFields []string
	// Capabilities computes user_capabilities; defaults derive from RBAC.
	Capabilities func(c *Ctx, obj Obj, perms map[string]bool) map[string]bool
	// Serialize post-processes a serialized object.
	Serialize func(c *Ctx, obj Obj)

	SubLists []*SubList
	Actions  []*Action
	// Relations are reverse/m2m relations usable in filters.
	Relations map[string]Relation

	// ReadOnly disables POST/PUT/PATCH/DELETE.
	ReadOnly bool
	NoCreate bool
	NoDelete bool
	// NoUpdate rejects PUT/PATCH on objects (e.g. jobs).
	NoUpdate   bool
	NoActivity bool
	// Copyable enables /copy/.
	Copyable bool
	// Versioned records a snapshot on every change and exposes /history/.
	Versioned bool
	// Managed adds a read-only managed_by field; objects with a non-empty
	// managed_by can only be changed by that source (see Ctx.Extra["managed_by"]).
	Managed bool
	// VersionExtra adds relation state (credentials, labels, ...) to snapshots.
	VersionExtra func(c *Ctx, obj Obj) (Obj, error)
	// RevertExtra restores relation state captured by VersionExtra.
	RevertExtra func(c *Ctx, obj Obj, snapshot Obj) error
	// AfterCopy copies relations (credentials, labels, ...) from src to the new object.
	AfterCopy func(c *Ctx, src, dst Obj) error

	fieldMap map[string]*Field
}

func (r *Resource) field(name string) *Field {
	return r.fieldMap[name]
}

func (r *Resource) URL(id any) string {
	return "/api/v2/" + r.Name + "/" + toStr(id) + "/"
}

// Ctx is the per-request context passed to resource hooks.
type Ctx struct {
	context.Context
	App  *App
	User *Principal
	Req  *http.Request
	// Tx, when set, is used for writes inside a transaction.
	Tx DBTX
	// Extra carries request-scoped values between hooks.
	Extra map[string]any
}

// DB returns the transaction if one is active, otherwise the pool.
func (c *Ctx) DB() DBTX {
	if c.Tx != nil {
		return c.Tx
	}
	return c.App.DB
}
