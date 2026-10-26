package resources

// Resources are registered in dependency order: later registrations may attach
// sublists to earlier ones.
func init() {
	registerUsers()
	registerOrganizations()
	registerFolders()
	registerCredentials()
	registerInfrastructure()
	registerInventories()
	registerRoles()
	registerActivity()
}
