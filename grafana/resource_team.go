package grafana

import (
	"errors"
	"fmt"
	"log"
	"strconv"

	gapi "github.com/micro23xd/go-grafana-api"

	"github.com/hashicorp/terraform/helper/schema"
)

type TeamUser struct {
	Id    int64
	Email string
}

type UserTeamChange struct {
	Type ChangeType
	User TeamUser
}

func ResourceTeam() *schema.Resource {
	return &schema.Resource{
		Create: CreateTeam,
		Read:   ReadTeam,
		Update: UpdateTeam,
		Delete: DeleteTeam,
		Exists: ExistsTeam,
		Importer: &schema.ResourceImporter{
			State: ImportTeam,
		},

		Schema: map[string]*schema.Schema{
			"name": {
				Type:     schema.TypeString,
				Required: true,
			},
			"email": {
				Type:     schema.TypeString,
				Optional: true,
			},
			"create_users": {
				Type:     schema.TypeBool,
				Optional: true,
				Default:  true,
			},
			"org_id": {
				Type:     schema.TypeInt,
				Optional: true,
				Default:  "1",
			},
			"users": {
				Type:     schema.TypeList,
				Optional: true,
				Elem: &schema.Schema{
					Type: schema.TypeString,
				},
			},
		},
	}
}

func CreateTeam(d *schema.ResourceData, meta interface{}) error {
	client := meta.(*gapi.Client)
	name := d.Get("name").(string)
	email := d.Get("email").(string)
	teamID, err := client.AddTeam(name, email)
	if err != nil && err.Error() == "409 Conflict" {
		return errors.New(fmt.Sprintf("Error: A Grafana team with the name '%s' and id '%d' already exists.", name, teamID))
	}
	if err != nil {
		return err
	}
	d.SetId(strconv.FormatInt(teamID, 10))
	return UpdateTeamMembers(d, meta)
}

func ReadTeam(d *schema.ResourceData, meta interface{}) error {
	client := meta.(*gapi.Client)
	teamId, _ := strconv.ParseInt(d.Id(), 10, 64)
	resp, err := client.Team(teamId)
	if err != nil && err.Error() == "404 Not Found" {
		log.Printf("[WARN] removing Team %s from state because it no longer exists in grafana", d.Id())
		d.SetId("")
		return nil
	}
	if err != nil {
		return err
	}
	d.Set("name", resp.Name)
	if err := ReadTeamUsers(d, meta); err != nil {
		return err
	}
	return nil
}

func UpdateTeam(d *schema.ResourceData, meta interface{}) error {
	client := meta.(*gapi.Client)
	teamId, _ := strconv.ParseInt(d.Id(), 10, 64)
	if d.HasChange("name") || d.HasChange("email") {
		name := d.Get("name").(string)
		email := d.Get("email").(string)
		err := client.UpdateTeam(teamId, name, email)
		if err != nil {
			return err
		}
	}
	return UpdateTeamMembers(d, meta)
}

func DeleteTeam(d *schema.ResourceData, meta interface{}) error {
	client := meta.(*gapi.Client)
	teamId, _ := strconv.ParseInt(d.Id(), 10, 64)
	return client.DeleteTeam(teamId)
}

func ExistsTeam(d *schema.ResourceData, meta interface{}) (bool, error) {
	client := meta.(*gapi.Client)
	teamId, _ := strconv.ParseInt(d.Id(), 10, 64)
	_, err := client.Team(teamId)
	if err != nil && err.Error() == "404 Not Found" {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, err
}

func ImportTeam(d *schema.ResourceData, meta interface{}) ([]*schema.ResourceData, error) {
	exists, err := ExistsTeam(d, meta)
	if err != nil || !exists {
		return nil, errors.New(fmt.Sprintf("Error: Unable to import Grafana Team: %s.", err))
	}
	d.Set("admin_user", "admin")
	d.Set("create_users", "true")
	err = ReadTeam(d, meta)
	if err != nil {
		return nil, err
	}
	return []*schema.ResourceData{d}, nil
}

func ReadTeamUsers(d *schema.ResourceData, meta interface{}) error {
	client := meta.(*gapi.Client)
	teamId, _ := strconv.ParseInt(d.Id(), 10, 64)
	teamUsers, err := client.TeamMembers(teamId)
	if err != nil {
		return err
	}
	var userMap []string

	grafAdmin := d.Get("admin_user")
	for _, teamUser := range teamUsers {
		if teamUser.Login != grafAdmin {
			// roleMap["Admin"] = append(roleMap["Admin"], teamUser.Email)
			userMap = append(userMap, teamUser.Email)
		}
	}
	d.Set("users", userMap)
	return nil
}

func UpdateTeamMembers(d *schema.ResourceData, meta interface{}) error {
	stateUsers, configUsers, err := collectTeamUsers(d)
	if err != nil {
		return err
	}
	changes := teamChanges(stateUsers, configUsers)
	teamId, _ := strconv.ParseInt(d.Id(), 10, 64)
	changes, err = addIdsToTeamChanges(d, meta, changes)
	if err != nil {
		return err
	}
	return applyTeamChanges(meta, teamId, changes)
}

func collectTeamUsers(d *schema.ResourceData) (map[string]TeamUser, map[string]TeamUser, error) {
	stateUsers, configUsers := make(map[string]TeamUser), make(map[string]TeamUser)

	// Get the lists of users read in from Grafana state (old) and configured (new)
	state, config := d.GetChange("users")
	for _, u := range state.([]interface{}) {
		email := u.(string)
		// Sanity check that a user isn't specified twice within an Team
		if _, ok := stateUsers[email]; ok {
			return nil, nil, errors.New(fmt.Sprintf("Error: User '%s' cannot be specified multiple times.", email))
		}
		stateUsers[email] = TeamUser{0, email}
	}
	for _, u := range config.([]interface{}) {
		email := u.(string)
		// Sanity check that a user isn't specified twice within an Team
		if _, ok := configUsers[email]; ok {
			return nil, nil, errors.New(fmt.Sprintf("Error: User '%s' cannot be specified multiple times.", email))
		}
		configUsers[email] = TeamUser{0, email}
	}

	return stateUsers, configUsers, nil
}

func teamChanges(stateUsers, configUsers map[string]TeamUser) []UserTeamChange {
	var changes []UserTeamChange
	for _, user := range configUsers {
		_, ok := stateUsers[user.Email]
		if !ok {
			// User doesn't exist in Grafana's state for the Team, should be added.
			changes = append(changes, UserTeamChange{Add, user})
			continue
		}
	}
	for _, user := range stateUsers {
		if _, ok := configUsers[user.Email]; !ok {
			// User exists in Grafana's state for the Team, but isn't
			// present in the Team configuration, should be removed.
			changes = append(changes, UserTeamChange{Remove, user})
		}
	}
	return changes
}

func addIdsToTeamChanges(d *schema.ResourceData, meta interface{}, changes []UserTeamChange) ([]UserTeamChange, error) {
	client := meta.(*gapi.Client)
	gUserMap := make(map[string]int64)
	gUsers, err := client.Users()
	if err != nil {
		return nil, err
	}
	for _, u := range gUsers {
		gUserMap[u.Email] = u.Id
	}
	var output []UserTeamChange
	create := d.Get("create_users").(bool)
	for _, change := range changes {
		id, ok := gUserMap[change.User.Email]
		if !ok && !create {
			return nil, errors.New(fmt.Sprintf("Error adding user %s. User does not exist in Grafana.", change.User.Email))
		}
		if !ok && create {
			id, err = createUser(meta, change.User.Email)
			if err != nil {
				return nil, err
			}
		}
		change.User.Id = id
		output = append(output, change)
	}
	return output, nil
}

func applyTeamChanges(meta interface{}, teamId int64, changes []UserTeamChange) error {
	var err error
	client := meta.(*gapi.Client)
	for _, change := range changes {
		u := change.User
		switch change.Type {
		case Add, Update:
			err = client.AddTeamMember(teamId, u.Id)
		case Remove:
			err = client.RemoveMemberFromTeam(teamId, u.Id)
		}
		if err != nil && err.Error() != "409 Conflict" {
			return err
		}
	}
	return nil
}
