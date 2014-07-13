package main

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/randallsquared/go-tigertonic"
	"github.com/randallsquared/gochute/profile"
)

type PhotoHandler struct {
}

type Photo struct {
	Id            int
	Created       time.Time
	Href, Caption string
}

type Profile struct {
	Id      int
	Created time.Time
	Email   *string
	Phone   *string
	Name    *string
	Photos  []Photo
	Flags   []profile.Flag
	Utypes  []profile.Utype
}

type Auth struct {
	Profile    int
	Hash       *string
	Username   *string
	Name       string
	Token      *string
	Created    *time.Time
	Updated    *time.Time
	LastAuth   *time.Time
	Authorized bool
}

type AuthChange struct {
	Hash       *string
	Username   *string
	Name       string
	Authorized bool
}

type Freetime struct {
	Start time.Time
	End   time.Time
}

// NewMessage doesn't need Invite, since it's either part of one or that info is in the URL.
// It doesn't need Sender, since that's the context Profile.  It doesn't need Id, since the
// client doesn't know it, yet.  It doesn't need Sent, since that's automatically generated
// at creation time.  It doesn't need much.
type NewMessage struct {
	Photo *int
	Body  string
}

// NewInvite, like NewMessage, is tailored to avoid duplicating information we already have,
// or which doesn't exist yet, such as the Organizer (context Profile), the Id, which will be
// created automatically, or Active, which must be true on creation.   We also don't want to
// pass the entire profiles of Attendees, since no changes can be made to them in any case,
// and that's a lot of extra network traffic which no one needs.
type NewInvite struct {
	Attendees []int // Profiles
	Start     time.Time
	End       *time.Time
	Place     string
	Message   *NewMessage
}

type Message struct {
	Id     int
	Sent   time.Time
	Sender Profile
	Photo  *Photo
	Body   string
}

type Attendee struct {
	Profile
	Status profile.Status
}

type Invite struct {
	Attendees []Attendee
	Id        int
	Organizer Profile
	Active    bool
	Start     time.Time
	End       *time.Time
	Created   time.Time
	Place     string
	Messages  []Message
}

func authenticate(r *http.Request) (http.Header, error) {
	token := r.Header.Get(ChuteToken)
	if token == "" {
		return nil, tigertonic.Unauthorized{errors.New("please log in")}
	}
	auth := new(profile.Auth)
	auth.Token = &token
	err := auth.Get()
	if err != nil || !auth.Authenticated() {
		return nil, tigertonic.Unauthorized{errors.New("please log in")}
	}
	c := tigertonic.Context(r).(*Context)
	c.Auth = auth
	c.Profile = new(profile.Profile)
	err = c.Profile.Get(auth)
	if err != nil {
		return nil, tigertonic.Unauthorized{errors.New("please log in")}
	}
	return nil, nil
}

func param(u *url.URL, param string) string {
	query := u.Query()
	return query.Get("{" + param + "}")
}

func findProfile(u *url.URL, c *Context) (*profile.Profile, func(string, ...interface{}) (int, http.Header, Response, error), error) {
	id := param(u, "id")
	if id == "" {
		return c.Profile, nil, nil
	}
	intId, err := strconv.Atoi(id)
	if err != nil {
		complaint := "'" + id + "' is not a valid Profile Id."
		return nil, error400, errors.New(complaint)
	}
	ip, err := profile.GetProfile(intId)
	if err != nil {
		if err.Error() == profile.NotFoundError {
			return nil, error404, errors.New("profile not found")
		}
		return nil, error500, err
	}
	return ip, nil, nil
}

func (m *Message) convert(im profile.Message) error {
	m.Id = im.Id
	m.Sent = im.Sent
	m.Body = im.Body

	ip, err := profile.GetProfile(im.Sender)
	if err != nil {
		return err
	}
	m.Sender = Profile{}
	err = m.Sender.convert(*ip)

	if im.Photo == nil {
		// short circuit out o' here; we're done
		return nil
	}
	ph, err := profile.GetPhoto(*im.Photo)
	if err != nil {
		return err
	}

	// dammit, we shouldn't have to keep running back to the database for this
	ip, err = profile.GetProfile(ph.Profile)
	if err != nil {
		return err
	}

	href := ph.GetExpiringUrl(ip.Folder)
	m.Photo = &Photo{ph.Id, ph.Created, href, ph.Caption}
	return nil
}

func (p *Profile) convert(ip profile.Profile) error {
	p.Id = ip.Id
	p.Created = ip.Created
	p.Email = ip.Email
	p.Phone = ip.Phone
	p.Name = ip.Name
	p.Flags = ip.Flags
	p.Utypes = ip.Utypes

	dbPhotos, err := ip.Photos()
	if err != nil {
		return err
	}
	for _, photo := range dbPhotos {
		href := photo.GetExpiringUrl(ip.Folder)
		p.Photos = append(p.Photos, Photo{photo.Id, photo.Created, href, photo.Caption})
	}
	return nil
}

func (i *Invite) convert(ii profile.Invite) error {
	i.Id = ii.Id
	i.Active = ii.Active
	i.Start = ii.Start
	i.End = ii.End
	i.Created = ii.Created
	i.Place = ii.Place

	// Organizer and Attendees are or contain Profiles, so there's some hoops to jump through
	ip, err := profile.GetProfile(ii.Organizer)
	if err != nil {
		return err
	}
	i.Organizer = Profile{}
	err = i.Organizer.convert(*ip)
	if err != nil {
		return err
	}

	for _, att := range ii.Attendees {
		p := Profile{}
		err = p.convert(att.Profile)
		if err != nil {
			return err
		}
		i.Attendees = append(i.Attendees, Attendee{p, att.Status})
	}

	for _, im := range ii.Messages {
		m := Message{}
		err = m.convert(im)
		if err != nil {
			return err
		}
		i.Messages = append(i.Messages, m)
	}

	return nil
}

func getInvite(u *url.URL, h http.Header, _ interface{}, c *Context) (int, http.Header, Response, error) {
	id := param(u, "id")
	intId, err := strconv.Atoi(id)
	if err != nil {
		return error400("'"+id+"' is not a valid Invite Id.", "Bad invite id.")
	}
	ii, err := profile.GetInvite(intId)
	if err != nil {
		return error500("db failure: p208", err.Error())
	}
	i := Invite{}
	err = i.convert(*ii)
	if err != nil {
		return error500("db failure: p204", err.Error())
	}
	return http.StatusOK, nil, i, nil
}

func cancelInvite(u *url.URL, h http.Header, _ interface{}, c *Context) (int, http.Header, Response, error) {
	id := param(u, "id")
	intId, err := strconv.Atoi(id)
	if err != nil {
		return error400("'"+id+"' is not a valid Invite Id.", "Bad invite id.")
	}
	ii, err := profile.GetInvite(intId)
	if err != nil {
		errString := err.Error()
		if errString == profile.NotFoundError {
			return error404("Invite not found.", errString)
		}
		return error500("db failure: p269", errString)
	}
	err = ii.Cancel()
	if err != nil {
		return error500("db failure: p273", err.Error())
	}
	ii.Active = false
	i := Invite{}
	err = i.convert(*ii)
	if err != nil {
		return error500("db failure: p279", err.Error())
	}
	return http.StatusOK, nil, i, nil
}

func addMessage(u *url.URL, h http.Header, m *NewMessage, c *Context) (int, http.Header, Response, error) {
	id := param(u, "id")
	intId, err := strconv.Atoi(id)
	if err != nil {
		return error400("'"+id+"' is not a valid Invite Id.", "Bad invite id.")
	}
	ii, err := profile.GetInvite(intId)
	if err != nil {
		errString := err.Error()
		if errString == profile.NotFoundError {
			return error404("Invite not found.", errString)
		}
		return error500("db failure: p268", errString)
	}
	// if there's a photo, check that it exists and is owned by us
	if m.Photo != nil {
		photoId := *m.Photo
		_, err := c.Profile.GetPhoto(photoId)
		if err != nil {
			return error400("'"+strconv.Itoa(photoId)+"' is not a valid Photo Id.", "Bad photo id")
		}
	}

	im := profile.Message{0, time.Now(), c.Profile.Id, ii.Id, m.Photo, m.Body}
	err = im.Create()
	if err != nil {
		return error500("db failure: p273", err.Error())
	}
	ii.RefreshMessages(nil)

	i := Invite{}
	err = i.convert(*ii)
	if err != nil {
		return error500("db failure: p280", err.Error())
	}
	return http.StatusOK, nil, i, nil

}

func changeStatus(u *url.URL, h http.Header, s *profile.Status, c *Context) (int, http.Header, Response, error) {
	status := *s
	if s == nil || !profile.Statuses[status] {
		complaint := "'" + string(status) + "' doesn't appear to be a valid status: "
		complaint += strings.Join(profile.StatusStrings(), ", ")
		return error400(complaint, "non-Status status update")
	}
	id := param(u, "id")
	intId, err := strconv.Atoi(id)
	if err != nil {
		return error400("'"+id+"' is not a valid Invite Id.", "Bad invite id.")
	}
	ii, err := profile.GetInvite(intId)
	if err != nil {
		errString := err.Error()
		if errString == profile.NotFoundError {
			return error404("Invite not found.", errString)
		}
		return error500("db failure: p270", errString)
	}
	err = ii.ChangeStatus(*c.Profile, status)
	if err != nil {
		return error500("db failure: p278", err.Error())
	}
	// let's avoid going back for another dozen db queries...
	for i := range ii.Attendees {
		if ii.Attendees[i].Id == c.Profile.Id {
			ii.Attendees[i].Status = status
		}
	}
	i := Invite{}
	err = i.convert(*ii)
	if err != nil {
		return error500("db failure: p289", err.Error())
	}
	return http.StatusOK, nil, i, nil
}

func addAttendees(u *url.URL, h http.Header, as []int, c *Context) (int, http.Header, Response, error) {
	id := param(u, "id")
	intId, err := strconv.Atoi(id)
	if err != nil {
		return error400("'"+id+"' is not a valid Invite Id.", "Bad invite id.")
	}
	ii, err := profile.GetInvite(intId)
	if err != nil {
		errString := err.Error()
		if errString == profile.NotFoundError {
			return error404("Invite not found.", errString)
		}
		return error500("db failure: p373", errString)
	}
	if ii.Organizer != c.Profile.Id {
		return error403("You are not the Organizer for this Invite.", "Bad organizer!")
	}

	var atts []profile.Attendee
	for _, a := range as {
		p, err := profile.GetProfile(a)
		if err != nil {
			complaint := "'" + strconv.Itoa(a) + "' is not a valid Profile id."
			return error400(complaint, "got a non-Profile Id for an attendee")
		}
		atts = append(atts, profile.Attendee{*p, profile.StatusPending})
	}

	err = ii.AddAttendees(atts)
	if err != nil {
		return error500("db failure: p392", err.Error())
	}

	err = ii.RefreshAttendees(nil)
	if err != nil {
		return error500("db failure: p397", err.Error())
	}

	i := Invite{}
	err = i.convert(*ii)
	if err != nil {
		return error500("db failure: p289", err.Error())
	}
	return http.StatusOK, nil, i, nil
}

func invite(u *url.URL, h http.Header, i *NewInvite, c *Context) (int, http.Header, Response, error) {
	if i.End != nil && !i.Start.Before(*i.End) {
		complaint := i.End.String() + " is not after " + i.Start.String()
		return error400(complaint, "got bad Invite representation")
	}
	if len(i.Attendees) < 1 {
		complaint := "There must be at least one attendee for an invite."
		return error400(complaint, "got Invite without any attendees")
	}
	var atts []profile.Attendee
	for _, att := range i.Attendees {
		p, err := profile.GetProfile(att)
		if err != nil {
			complaint := "'" + strconv.Itoa(att) + "' is not a valid Profile id."
			return error400(complaint, "got a non-Profile Id for an attendee")
		}
		atts = append(atts, profile.Attendee{*p, profile.StatusPending})
	}
	ii := profile.Invite{}
	ii.Organizer = c.Profile.Id
	ii.Active = true
	ii.Start = i.Start
	ii.End = i.End
	ii.Created = time.Now()
	ii.Place = i.Place
	err := ii.Create()
	if err != nil {
		return error500("db failure: p175", err.Error())
	}
	if i.Message != nil {
		m := profile.Message{0, time.Now(), c.Profile.Id, ii.Id, i.Message.Photo, i.Message.Body}
		err := m.Create()
		if err != nil {
			return error500("db failure: p245", err.Error())
		}
	}
	newI, err := profile.GetInvite(ii.Id)
	if err != nil {
		return error500("db failure: p250", err.Error())
	}
	out := Invite{}
	err = out.convert(*newI)
	if err != nil {
		return error500("db failure: p227", err.Error())
	}

	return http.StatusOK, nil, out, nil
}

// createProfile receives a hash and an optional username.
// If there is a username, it must be unique.
func createProfile(u *url.URL, h http.Header, r *Auth) (int, http.Header, Response, error) {
	var err error
	p := new(profile.Profile)
	a := profile.NewAuth(r.Hash, r.Username)
	// if Getting an Auth succeeds, there was an existing row
	err = a.Get()
	if err == nil {
		return error400("auth exists", "hash:", *r.Hash)
	}
	a.Name = r.Name

	err = p.Create()
	if err != nil {
		return error500("db failure: p56", err.Error())
	}
	a.Profile = p.Id

	err = a.Create()
	if err != nil {
		return error500("db failure: p62", err.Error())
	}

	// if all is well...
	oh := http.Header{}
	oh.Add(ChuteToken, *a.Token)
	response := Profile{Id: p.Id, Created: p.Created}
	return http.StatusCreated, oh, response, nil
}

func createAuth(u *url.URL, h http.Header, r *AuthChange, c *Context) (int, http.Header, Response, error) {
	a := profile.NewAuth(r.Hash, r.Username)
	err := a.Get()
	if err != nil {
		// this auth doesn't already exist
		a.Name = r.Name
		a.Authorized = r.Authorized
		a.Profile = c.Profile.Id
		a.InHash = []byte(*r.Hash)
		err = a.Create()
		if err != nil {
			return error500("db failure: p73", err.Error())
		}
	} else if a.Username != nil && a.Profile != c.Profile.Id {
		// this auth exists and is not a device and it wasn't ours; error
		return error400("unauthorized access")
	} else {
		log.Println("Got an auth: ", a)
		// this auth exists and it's ours (or a device that's about to be ours...)
		a.Name = r.Name
		a.Authorized = r.Authorized
		a.Profile = c.Profile.Id
		log.Println("Auth about to save: ", a)
		err = a.Save()
		if err != nil {
			return error500("db failure: p83", err.Error())
		}
	}
	return getAuths(u, h, nil, c)
}

func login(u *url.URL, h http.Header, r *Auth) (int, http.Header, Response, error) {
	auth := profile.NewAuth(r.Hash, r.Username)
	err := auth.Get()
	if err != nil || !auth.Authenticated() {
		return error401("login failure", "hash:", *r.Hash)
	}
	token, err := auth.Login()
	if err != nil {
		return error500("db failure: p137", err.Error())
	}

	oh := http.Header{}
	oh.Add(ChuteToken, token)
	return http.StatusOK, oh, struct{}{}, nil

}

func logout(u *url.URL, h http.Header, r *Auth, c *Context) (int, http.Header, Response, error) {
	err := c.Auth.Logout()
	if err != nil {
		return error500("db failure: p110", err.Error())
	}
	return http.StatusOK, nil, struct{}{}, nil

}

func getAuths(u *url.URL, h http.Header, _ interface{}, c *Context) (int, http.Header, Response, error) {
	var out []Auth
	auths, err := c.Profile.GetAuths()
	if err != nil {
		return error500("db failure: p102", err.Error())
	}
	for _, auth := range auths {
		out = append(out, Auth{auth.Profile, nil, auth.Username, auth.Name, auth.Token, auth.Created, auth.Updated, auth.LastAuth, auth.Authorized})
	}
	return http.StatusOK, nil, out, nil
}

/*
getMyInvitesBySearch pulls from the URL (and can only see invites to which this user is a party).
'status': one of the profile.Status constants.
'from': ISO-8601 timestamp to find invites which start on or after (only the date is used).
*/
func getMyInvitesBySearch(u *url.URL, h http.Header, _ interface{}, c *Context) (int, http.Header, Response, error) {
	var (
		is     []Invite
		i      Invite
		err    error
		status *profile.Status = nil
		from   time.Time
	)
	query := u.Query()
	if s := query.Get("status"); len(s) > 0 {
		tmpStatus := profile.Status(s)
		status = &tmpStatus
	}
	if t := query.Get("from"); len(t) > 0 {
		from, err = time.Parse(time.RFC3339Nano, t)
		if err != nil {
			return error400("didn't understand '"+t+"' as a search time", err.Error())
		}
	} else {
		from = time.Now()
	}
	iis, err := c.Profile.GetInvites(status, from)
	if err != nil {
		return error500("db failure: p409", err.Error())
	}
	for _, ii := range iis {
		i = Invite{}
		err = i.convert(ii)
		if err != nil {
			return error500("db failure: p418", err.Error())
		}
		is = append(is, i)
	}
	return http.StatusOK, nil, is, nil
}

/*
getProfilesBySearch pulls from the URL
'from': ISO-8601 timestamp to find profiles with free time surrounding.
'type': the Profile type to search for (multiple specifications are ORed together)
'flag': the flags to search for (multiple specifications are ANDed together)
*/
func getProfilesBySearch(u *url.URL, h http.Header, _ interface{}, c *Context) (int, http.Header, Response, error) {
	var (
		p    Profile
		ps   []Profile
		from time.Time
		err  error
	)
	query := u.Query()
	if f := query.Get("from"); len(f) > 0 {
		from, err = time.Parse(time.RFC3339Nano, f)
		if err != nil {
			return error400("didn't understand '"+f+"' as a search time", err.Error())
		}
	} else {
		from = time.Now()
	}
	flags := query["flag"]
	for _, flag := range flags {
		if _, err := strconv.ParseInt(flag, 10, 0); err != nil {
			return error400("didn't understand '"+flag+"' as a flag", err.Error())
		}
	}
	utypes := query["type"]
	for _, t := range utypes {
		if _, err := strconv.ParseInt(t, 10, 0); err != nil {
			return error400("didn't understand '"+t+"' as a profile type", err.Error())
		}
	}
	// get all profiles with freetimes surrounding this 'from'
	// we start with the autenticated profile to get the lat and long without having to pass it
	ips, err := c.Profile.Search(from, utypes, flags)
	if err != nil {
		return error500("db failure: p205", err.Error())
	}

	for _, ip := range ips {
		p = Profile{}
		err = p.convert(ip)
		if err != nil {
			return error500("db failure: p247", err.Error())
		}
		ps = append(ps, p)
	}

	return http.StatusOK, nil, ps, nil
}

func getProfile(u *url.URL, h http.Header, _ interface{}, c *Context) (int, http.Header, Response, error) {
	var p Profile
	var ip *profile.Profile
	ip, errType, err := findProfile(u, c)
	if err != nil {
		return errType("profile not found", err.Error())
	}
	p = Profile{}
	err = p.convert(*ip)
	if err != nil {
		return error500("db failure: p188", err.Error())
	}
	return http.StatusOK, nil, p, nil
}

func getFlags(u *url.URL, h http.Header, _ interface{}) (int, http.Header, Response, error) {
	flags, err := profile.GetFlags()
	if err != nil {
		return error500("db failure: p220", err.Error())
	}
	return http.StatusOK, nil, flags, nil
}

func getTypes(u *url.URL, h http.Header, _ interface{}) (int, http.Header, Response, error) {
	types, err := profile.GetTypes()
	if err != nil {
		return error500("db failure: p228", err.Error())
	}
	return http.StatusOK, nil, types, nil
}

func getFreetime(u *url.URL, h http.Header, _ interface{}, c *Context) (int, http.Header, Response, error) {
	var fs []Freetime
	p, errType, err := findProfile(u, c)
	if err != nil {
		return errType("profile not found", err.Error())
	}

	profileFs, err := p.GetFreetimes()
	if err != nil {
		return error500("db failure: p212", err.Error())
	}
	for _, f := range profileFs {
		fs = append(fs, Freetime{f.Start, f.End})
	}
	return http.StatusOK, nil, fs, nil
}

func removeAllFreetime(u *url.URL, h http.Header, _ interface{}, c *Context) (int, http.Header, Response, error) {
	err := c.Profile.RemoveAllFreetime()
	if err != nil {
		return error500("db failure: p238", err.Error())
	}
	return http.StatusNoContent, nil, nil, nil
}

func removeFreetime(u *url.URL, h http.Header, _ interface{}, c *Context) (int, http.Header, Response, error) {
	var err error
	s := param(u, "start")
	start, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return error500("didn't understand '"+s+"' as a start time", err.Error())
	}
	err = c.Profile.RemoveFreetime(start)
	if err != nil {
		return error500("db failure: p253", err.Error())
	}
	return http.StatusNoContent, nil, nil, nil
}

func createFreetime(u *url.URL, h http.Header, fs []Freetime, c *Context) (int, http.Header, Response, error) {
	// need to ensure that start is before end
	for _, f := range fs {
		if !f.Start.Before(f.End) {
			complaint := f.End.String() + " is not after " + f.Start.String()
			return error400(complaint, "got bad Freetime representation")
		}
		// I'm not doing the create/insert here because I don't want to insert a random number
		// of these before having an error.   So we have to loop over range fs twice, which is
		// not very nice, but not sure how else to handle it.
	}
	var err error
	for _, f := range fs {
		err = c.Profile.NewFreetime(f.Start, f.End)
		if err != nil {
			if err.Error() == profile.DuplicateFreetimeError {
				err = c.Profile.UpdateFreetime(f.Start, f.End)
				if err != nil {
					return error500("db failure: p261", err.Error())
				}
				continue
			}
			return error500("db failure: p223", err.Error())
		}
	}
	return getFreetime(u, h, nil, c)
}

func updateProfile(u *url.URL, h http.Header, p *Profile, c *Context) (int, http.Header, Response, error) {
	// we're already authed, so we just have to update and save, right?
	c.Profile.Email = p.Email
	c.Profile.Phone = p.Phone
	c.Profile.Name = p.Name
	c.Profile.Flags = p.Flags
	c.Profile.Utypes = p.Utypes
	err := c.Profile.Save()
	if err != nil {
		return error500("db failure: p308", err.Error())
	}
	out := Profile{}
	err = out.convert(*c.Profile)
	if err != nil {
		return error500("db failure: p313", err.Error())
	}
	return http.StatusOK, nil, out, nil
}

func removePhoto(u *url.URL, h http.Header, _ interface{}, c *Context) (int, http.Header, Response, error) {
	id := u.Query().Get("{id}")
	intId, err := strconv.Atoi(id)
	if err != nil {
		return error400("'"+id+"' is not a valid Photo id.", "Bad photo id.")
	}
	photo, err := c.Profile.GetPhoto(intId)
	if err != nil {
		return error404("photo not found", err.Error())
	}
	err = photo.Remove()
	if err != nil {
		return error500("photo removal error: p220", err.Error())
	}
	return http.StatusNoContent, nil, nil, nil
}

// PhotoHandler.ServeHTTP currently handles only the first file uploaded.
func (ph PhotoHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var complaint string
	file, meta, err := r.FormFile("photo")
	if err != nil {
		complaint = `{"error": "file upload issue: p200"}`
		log.Println(complaint, err.Error())
		w.WriteHeader(500)
		w.Write([]byte(complaint + "\n"))
		return
	}
	typeArray := meta.Header["Content-Type"]
	if len(typeArray) < 1 || !allowedPhotoTypes[typeArray[0]] {
		complaint = `{"error": "please upload only an image file in jpg, gif, or png"}`
		log.Println(complaint)
		w.WriteHeader(400)
		w.Write([]byte(complaint + "\n"))
		return
	}
	ctype := typeArray[0]

	fmt.Println(r.FormValue("caption"))
	c := tigertonic.Context(r).(*Context)
	photo := c.Profile.NewPhoto(r.FormValue("caption"))
	count, err := photo.Save(c.Profile.Folder, ctype, file)
	if err != nil {
		complaint = `{"error": "file upload issue: p244"}`
		log.Println(complaint, err.Error())
		w.WriteHeader(500)
		w.Write([]byte(complaint + "\n"))
		return
	}
	strCount := strconv.FormatInt(count, 10)
	strId := strconv.Itoa(photo.Id)
	message := `{"status": "ok", "id": ` + strId + `, "uploaded": ` + strCount + "}\n"
	_, err = w.Write([]byte(message))
	if err != nil {
		log.Println("WTF? p226", err.Error())
	}
}

/*

func blockProfile(u *url.URL, h http.Header, _ interface{}) (int, http.Header, interface{}, error) {
	return http.StatusOK, nil, nil, nil
}

func unBlockProfile(u *url.URL, h http.Header, _ interface{}) (int, http.Header, interface{}, error) {
	return http.StatusOK, nil, nil, nil
}
*/
