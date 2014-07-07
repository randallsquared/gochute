// Package profile implements the database handling for user profiles and related items,
// such as authentication and photos.
package profile

import (
	"crypto/sha512"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"mime/multipart"
	"os"
	"strings"
	"time"

	"code.google.com/p/go.crypto/bcrypt"
	"github.com/coopernurse/gorp"
	_ "github.com/lib/pq"
	"github.com/randallsquared/go-tigertonic"
	"launchpad.net/goamz/aws"
	"launchpad.net/goamz/s3"
)

// probably many of these should be configurable, instead
const (
	TokenLength            = 40
	UsernamelessSalt       = "nx7sn3ks67La72&2"
	NotFoundError          = "Item Not Found"
	DuplicateFreetimeError = "Duplicate Freetime Found"
	Region                 = "us-east-1"
	S3Endpoint             = "https://s3.amazonaws.com"
	AccessKey              = "AKIAJOIUUX2GBV3PYJSQ"
	SecretKey              = "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"
	BaseBucket             = "chuteprofilephotos"
	PhotoExpirationSeconds = 300
)

type Status string

const (
	StatusPending  Status = "Pending"
	StatusAccepted Status = "Accepted"
	StatusDeclined Status = "Declined"
)

var (
	dbmap    *gorp.DbMap
	s3Photos *s3.S3
	Statuses map[Status]bool = map[Status]bool{
		StatusPending:  true,
		StatusAccepted: true,
		StatusDeclined: true}
)

// Auth keeps track of an authentication method which is used to log into a Profile.
// Every Auth has a Hash derived from the InHash sent to us by the client: if there
// is a Username, then we grab the Auth that matches that Username and use bcrypt to
// verify the password represented by the InHash; if there is no Username, then we
// verify that we have a matching Auth for the device identity represented by the
// InHash.
//
// Any number of Auths can be attached to the same Profile, and any of them can
// be Authorized or not, individually.   Clients will likely only provide the UI
// for a single Username/Password Auth.
type Auth struct {
	InHash     []byte `db:"-"`
	Hash       []byte
	Created    *time.Time
	Updated    *time.Time
	LastAuth   *time.Time
	Profile    int
	Name       string
	Username   *string
	Token      *string
	Authorized bool
}

// Profile is the central access to user information, and the receiver for most methods
// in the profile package.
type Profile struct {
	Auth      *Auth   `db:"-"`
	Utypes    []Utype `db:"-"`
	Flags     []Flag  `db:"-"`
	Id        int
	Created   time.Time
	Updated   time.Time
	Latitude  float32
	Longitude float32
	Email     *string
	Phone     *string
	Name      *string
	Folder    string
}

// Photo keeps track of information about uploaded photos, including the final location
// of the binary data (currently on S3).
type Photo struct {
	Id      int
	Profile int
	Created time.Time
	Href    string
	Caption string
}

// Freetime keeps track of each instance of free time specified by the user.  When an
// invite takes up some of this free time, it's initially up to the user to manually
// make themself unavailable, if desired.  A later feature might involve automatically
// updating Freetime when an invite is accepted.
//
// Freetimes are unique in (Profile, Start).  This means that we can update a Freetime
// given those data and a new End, without having to expose the Id through JSON.
type Freetime struct {
	Id      int
	Profile int
	Created time.Time
	Start   time.Time `db:"freestart"`
	End     time.Time `db:"freeend"`
}

// Utype is just the singular-at-first profile type of the user.  We're building this
// as though it's a one-to-many, though, since an MUA might also be a model, etc.
type Utype struct {
	Id   int
	Name string
}

// Flag represents any search flag such as "does nude modeling".
type Flag struct {
	Id   int
	Name string
}

type Attendee struct {
	Profile
	Status Status
}

// Invite represents an invitation to a shoot sent by an organizer to all attendees.
//
// Note that while Start is required, an Invite may be missing an End.
type Invite struct {
	Attendees []Attendee `db:"-"` // built with PostGet
	Id        int
	Organizer int
	Active    bool
	Start     time.Time  `db:"invitestart"`
	End       *time.Time `db:"inviteend"`
	Created   time.Time
	Place     string
	Messages  []Message `db:"-"`
}

// Message represents some text and optionally a photo which is visible to everyone
// involved in an Invite.  Later when we have private messaging, we will use another
// table/struct to represent that.
type Message struct {
	Id     int
	Sent   time.Time
	Sender int  // Profile
	Invite int  // Invite
	Photo  *int // Photo
	Body   string
}

func init() {
	db, err := sql.Open("postgres", "postgres://chute:chute@localhost/chute")
	if err != nil {
		log.Panicln(err, "sql.Open failed")
	}
	dbmap = &gorp.DbMap{Db: db, Dialect: gorp.PostgresDialect{}}
	dbmap.TraceOn("[gorp]", log.New(os.Stdout, "chute:", log.Lmicroseconds))
	dbmap.AddTableWithName(Profile{}, "profile").SetKeys(true, "Id")
	dbmap.AddTableWithName(Auth{}, "auth").SetKeys(false, "Hash")
	dbmap.AddTableWithName(Photo{}, "photo").SetKeys(true, "Id")
	dbmap.AddTableWithName(Freetime{}, "free").SetKeys(true, "Id")
	dbmap.AddTableWithName(Utype{}, "utype").SetKeys(true, "Id")
	dbmap.AddTableWithName(Flag{}, "flag").SetKeys(true, "Id")
	dbmap.AddTableWithName(Invite{}, "Invite").SetKeys(true, "Id")
	dbmap.AddTableWithName(Message{}, "message").SetKeys(true, "Id")

	auth := aws.Auth{AccessKey, SecretKey}
	s3Photos = s3.New(auth, aws.Region{Name: Region, S3Endpoint: S3Endpoint})
}

// token returns a generic string of characters.
func token() string {
	return tigertonic.RandomBase62String(TokenLength)
}

// StatusStrings returns an array of strings of the Statuses.
func StatusStrings() []string {
	out := []string{}
	for k := range Statuses {
		out = append(out, string(k))
	}
	return out
}

// Save takes the Profile Folder, a content-type (passed to S3), and the binary file
// data, and saves the file to S3, then saving the S3 information to the database.
// It returns the number of bytes sent to S3, for no particular reason, and any error.
func (p *Photo) Save(folder, ctype string, file multipart.File) (int64, error) {
	end, err := file.Seek(0, 2)
	if err != nil {
		return 0, err
	}
	_, err = file.Seek(0, 0)
	if err != nil {
		return 0, err
	}
	// ensure that folder actually exists in S3, here!
	b := s3Photos.Bucket(BaseBucket + "/" + folder)
	// ensure that folder exists... this doesn't error if it's already there
	err = b.PutBucket(s3.Private)
	if err != nil {
		return 0, err
	}
	err = b.PutReader(p.Href, file, end, ctype, s3.Private)
	if err != nil {
		return 0, err
	}

	err = dbmap.Insert(p)
	if err != nil {
		return 0, err
	}
	return end, nil
}

// GetExpiringUrl takes the Profile Folder and returns a signed URL that will work for
// PhotoExpirationSeconds seconds.
func (p *Photo) GetExpiringUrl(folder string) string {
	b := s3Photos.Bucket(BaseBucket + "/" + folder)
	now := time.Now()
	return b.SignedURL(p.Href, now.Add(time.Duration(PhotoExpirationSeconds)*time.Second))

}

// Remove deletes a Photo from the database (but currently NOT S3).
func (p *Photo) Remove() error {
	// but first, we have to make sure it's not anywhere in Messages...
	_, err := dbmap.Exec("update message set photo = null where photo = $1", p.Id)
	if err != nil {
		return err
	}
	count, err := dbmap.Delete(p)
	if err != nil {
		return err
	}
	if count != 1 {
		return errors.New("remove Photo didn't delete 1 row? count: " + string(count))
	}
	return nil
}

// Search is the main search of the app, taking a time, and the types and flags to search for,
// and returning a list of Profiles free at that time and matching the types and flags sent.
//
// 'from' is treated as a time between the free start and free end times.
// 'utypes' are treated as OR; any Profile Utype can match.
// 'flags' are treated as AND; all flags must match.
func (p *Profile) Search(from time.Time, utypes, flags []string) ([]Profile, error) {
	// TODO: handle lat and long!
	var ps []Profile
	q := `
select profile.* from free inner join profile on (free.profile = profile.id) 
where freestart < :from and :from < freeend 
    `
	params := map[string]interface{}{}
	params["from"] = from

	for _, v := range flags {
		n := token()
		q += "\nand profile in (select profile from profile_flag where flag = :" + n + ")\n"
		params[n] = v
	}
	if len(utypes) > 0 {
		var fs []string
		for _, v := range utypes {
			n := token()
			fs = append(fs, "utype = :"+n)
			params[n] = v
		}
		ors := strings.Join(fs, " or ")
		q += "\nand profile in (select profile from profile_utype where " + ors + ")\n"
	}
	_, err := dbmap.Select(&ps, q, params)
	if err != nil {
		return ps, err
	}

	for i := range ps {
		err := ps[i].PostGet(dbmap) // not done in this case by gorp, sigh
		if err != nil {
			return []Profile{}, err
		}
	}
	return ps, nil
}

// UpdateFreetime changes the End of a Freetime given the receiving Profile and Start.
func (p *Profile) UpdateFreetime(Start, End time.Time) error {
	ft := Freetime{}
	err := dbmap.SelectOne(&ft, "select * from free where profile = $1 and freestart = $2", p.Id, Start)
	if err != nil {
		return err
	}
	ft.End = End
	count, err := dbmap.Update(&ft)
	if err != nil {
		return err
	}
	if count != 1 {
		return errors.New("update Freetime didn't update 1 row? count: " + string(count))
	}
	return nil
}

// RemoveallFreetime clears all Freetimes from the receiver.
func (p *Profile) RemoveAllFreetime() error {
	_, err := dbmap.Exec("delete from free where profile = $1", p.Id)
	return err
}

// RemoveFreetime clears a single Freetime from the receiver.
func (p *Profile) RemoveFreetime(Start time.Time) error {
	_, err := dbmap.Exec("delete from free where profile = $1 and freestart = $2", p.Id, Start)
	return err
}

// NewFreetime creates a new Freetime and saves it in the database.
// If the new Freetime has the same receiving Profile and Start time as another Freetime,
// it is an error.
func (p *Profile) NewFreetime(Start, End time.Time) error {
	ft := Freetime{0, p.Id, time.Now(), Start, End}
	err := dbmap.Insert(&ft)
	if err != nil {
		message := err.Error()
		if strings.Index(message, "violates unique constraint") > -1 {
			return errors.New(DuplicateFreetimeError)
		}
		return err
	}
	return nil
}

// GetFreetimes returns an array of Freetime from today forward.
func (p *Profile) GetFreetimes() ([]Freetime, error) {
	fs := []Freetime{}
	_, err := dbmap.Select(&fs, "select * from free where profile = $1 and freestart > current_date order by freestart asc", p.Id)
	return fs, err
}

// NewPhoto initializes a new Photo with a randomish Href.  It does not save.
// The Href only has to be unique within the folder of the receiving Profile, though it
// is probably unique across all tokens generated.
func (p *Profile) NewPhoto(caption string) Photo {
	return Photo{0, p.Id, time.Now(), token(), caption}
}

// Photos returns an array of all Photos for this profile, in no particular order.
func (p *Profile) Photos() ([]Photo, error) {
	ps := []Photo{}
	_, err := dbmap.Select(&ps, "select * from photo where profile = $1", p.Id)
	return ps, err
}

// NewAuth initializes a new Auth given a client hash, and optionally a username, which
// may be nil to indicate that this is not an Auth with a Username.
func NewAuth(h, u *string) Auth {
	return Auth{InHash: []byte(*h), Username: u}
}

// Create saves an Invite.
func (i *Invite) Create() error {
	return dbmap.Insert(i)
}

// Create saves a Message
func (m *Message) Create() error {
	return dbmap.Insert(m)
}

// Save saves a Profile to the database, ensuring that Updated is current.
func (p *Profile) Save() error {
	p.Updated = time.Now()
	count, err := dbmap.Update(p)
	if err != nil {
		return err
	}
	if count != 1 {
		return errors.New("update Profile didn't update 1 row? count: " + string(count))
	}
	return nil
}

// Save saves an Auth to the database, ensuring that Updated is current.
func (a *Auth) Save() error {
	now := time.Now()
	a.Updated = &now
	count, err := dbmap.Update(a)
	if err != nil {
		return err
	}
	if count != 1 {
		return errors.New("update Auth didn't update 1 row? count: " + string(count))
	}
	return nil
}

// Create does some pre-insert work to get timestamps and tokens in the right state.
//
// We could do this all in Save by using a PreInsert method and checking for Created!=nil
// or whatever, however, the fact that Update and Insert don't have the same signature
// makes that more pain than it's worth; it actually increases line count at merely an
// arguable increase in consistency.
func (a *Auth) Create() error {
	now := time.Now()
	t := token()
	a.Created = &now
	a.Updated = &now
	a.LastAuth = &now
	a.Token = &t
	a.Authorized = true
	h, err := hash(a.InHash, a.Username)
	if err != nil {
		return err
	}
	a.Hash = h
	return dbmap.Insert(a)
}

// Create does some pre-insert work to get timestamps and the Folder in the right state.
func (p *Profile) Create() error {
	t := time.Now()
	p.Created = t
	p.Updated = t
	p.Folder = token()
	return dbmap.Insert(p)
}

// Scan exists only to convert from the SQL result of []uint8 to a Status.
func (s *Status) Scan(src interface{}) error {
	switch src := src.(type) {
	default:
		return errors.New(fmt.Sprintf("unexpected type %T", src))
	case []uint8:
		tmp := Status(string(src))
		s = &tmp
	}
	return nil
}

func (i *Invite) RefreshMessages(s *gorp.SqlExecutor) error {
	var db gorp.SqlExecutor
	if s == nil {
		db = dbmap
	} else {
		db = *s
	}
	i.Messages = []Message{}
	query := "select * from message where invite = $1 order by id asc"
	_, err := db.Select(&i.Messages, query, i.Id)
	return err
}

func (i *Invite) RefreshAttendees(s *gorp.SqlExecutor) error {
	var db gorp.SqlExecutor
	if s == nil {
		db = dbmap
	} else {
		db = *s
	}
	i.Attendees = []Attendee{}
	query := "select profile.*, status from profile inner join profile_invite on (profile = id) where invite = $1"
	_, err := db.Select(&i.Attendees, query, i.Id)
	if err != nil {
		return err
	}
	for j := range i.Attendees {
		err := i.Attendees[j].Profile.PostGet(dbmap) // not done in this case by gorp, sigh
		if err != nil {
			return err
		}
	}
	return err
}

// PostGet sets the Attendees information on the newly instantiated Invite.
func (i *Invite) PostGet(s gorp.SqlExecutor) error {
	err := i.RefreshAttendees(&s)
	if err != nil {
		return err
	}
	return i.RefreshMessages(&s)
}

// PostGet sets Utype and Flag information on the newly instantiated Profile.
func (p *Profile) PostGet(s gorp.SqlExecutor) error {
	var err error
	fq := "select flag.* from flag inner join profile_flag on (flag = id) where profile = $1"
	_, err = s.Select(&p.Flags, fq, p.Id)
	if err != nil {
		return err
	}

	tq := "select utype.* from utype inner join profile_utype on (utype = id) where profile = $1"
	_, err = s.Select(&p.Utypes, tq, p.Id)
	if err != nil {
		return err
	}

	return nil
}

// AddAttendees adds attendees to an Invite, ignoring duplicates.
func (i *Invite) AddAttendees(as []Attendee) error {
	query := "insert into profile_invite values ($1, $2, $3)"
	for _, a := range as {
		_, err := dbmap.Exec(query, a.Id, i.Id, string(a.Status))
		if err != nil {
			errString := err.Error()
			if strings.Index(errString, "duplicate key value") > -1 {
				// ignored, because it doesn't hurt anything to just POST the whole list
				continue
			}
			return err
		}
	}
	return nil
}

// PostInsert ensures that the Attendees list is set in the database
func (i *Invite) PostInsert(s gorp.SqlExecutor) error {
	query := "insert into profile_invite values ($1, $2, $3)"
	for _, a := range i.Attendees {
		_, err := s.Exec(query, a.Id, i.Id, string(a.Status))
		if err != nil {
			return err
		}
	}
	return nil
}

// PostInsert ensures that the Flags and Utypes are recorded in the database.
func (p *Profile) PostInsert(s gorp.SqlExecutor) error {
	// flags
	for _, flag := range p.Flags {
		_, err := s.Exec("insert into profile_flag values ($1, $2)", flag.Id, p.Id)
		if err != nil {
			return err
		}
	}
	// types
	if len(p.Utypes) == 0 {
		p.Utypes = append(p.Utypes, Utype{1, "Model"})
	}
	for _, t := range p.Utypes {
		_, err := s.Exec("insert into profile_utype values ($1, $2)", t.Id, p.Id)
		if err != nil {
			return err
		}
	}
	return nil
}

// PostUpdate clears Flag and Utype information and then sets it correctly.  This
// could definitely be a bit lighter-touch, in the sense that we could diff the
// rows against the struct and only delete and insert some rows, but this might
// actually be faster anyway (and PostgreSQL doesn't have a handy upsert syntax,
// so, whatever).
func (p *Profile) PostUpdate(s gorp.SqlExecutor) error {
	var err error
	// flags
	_, err = s.Exec("delete from profile_flag where profile = $1", p.Id)
	if err != nil {
		return err
	}
	for _, flag := range p.Flags {
		_, err = s.Exec("insert into profile_flag values ($1, $2)", flag.Id, p.Id)
		if err != nil {
			return err
		}
	}
	// types
	if len(p.Utypes) == 0 {
		return errors.New("Profile utype cannot be empty")
	}
	_, err = s.Exec("delete from profile_utype where profile = $1", p.Id)
	if err != nil {
		return err
	}
	for _, t := range p.Utypes {
		_, err = s.Exec("insert into profile_utype values ($1, $2)", t.Id, p.Id)
		if err != nil {
			return err
		}
	}
	return nil
}

// GetFlags returns an array of all possible Flags.
func GetFlags() ([]Flag, error) {
	var fs []Flag
	_, err := dbmap.Select(&fs, "select * from flag order by id asc")
	return fs, err
}

// GetTypes returns an array of all possible Utypes.
func GetTypes() ([]Utype, error) {
	var ts []Utype
	_, err := dbmap.Select(&ts, "select * from utype order by id asc")
	return ts, err
}

// hash returns the canonical Hash for an Auth (why is this not a method on Auth?)
// If the second argument 'Username' is nil, this uses SHA512; otherwise bcrypt.
// We can't use bcrypt for auths without username because there'd be no way to
// know which one to get to test!  On the other hand, we really do want to use
// bcrypt in the case where this is someone's password, so we're stuck with two
// methods.
func hash(h []byte, u *string) ([]byte, error) {
	if u == nil {
		sum := sha512.Sum512([]byte(UsernamelessSalt + string(h)))
		return []byte(hex.EncodeToString(sum[0:64])), nil
	}
	return bcrypt.GenerateFromPassword(h, bcrypt.DefaultCost)

}

func (i *Invite) Cancel() error {
	_, err := dbmap.Exec("update invite set active = false where id = $1", i.Id)
	return err
}

// GetPhoto returns a single Photo from the database.
// This is used to verify that the Photo exists for adding to Messages.
func (p *Profile) GetPhoto(id int) (Photo, error) {
	photo := Photo{}
	err := dbmap.SelectOne(&photo, "select * from photo where id = $1 and profile = $2", id, p.Id)
	if err != nil {
		message := err.Error()
		if strings.Index(message, "no rows in result") > -1 {
			return photo, errors.New(NotFoundError)
		}
	}
	return photo, err
}

// GetAuths returns a array of all Auths for a given Profile.
func (p *Profile) GetAuths() ([]Auth, error) {
	auths := []Auth{}
	_, err := dbmap.Select(&auths, "select * from auth where profile = $1", p.Id)
	return auths, err
}

// ChangeStatus takes an Profile and a new Status and sets it in the database.
func (i *Invite) ChangeStatus(p Profile, s Status) error {
	query := "update profile_invite set status = $1 where profile = $2 and invite = $3"
	_, err := dbmap.Exec(query, string(s), p.Id, i.Id)
	return err
}

// GetInvites takes an optional status and a time and returns an array of Invites that match.
// An empty status is treated as meaning that Invites which have the Profile as an Organizer
// are desired.
func (p *Profile) GetInvites(status *Status, from time.Time) ([]Invite, error) {
	var query string
	is := []Invite{}
	params := map[string]interface{}{"profile": p.Id, "from": from.Format("2006-01-02")}
	if status != nil {
		query = "select invite.* from invite inner join profile_invite on (id = invite) where "
		query += " status = :status and "

		s := string(*status)
		params["status"] = s

		query += " profile = :profile and invitestart > :from "
	} else {
		query = " select invite.* from invite where "
		query += " organizer = :profile and invitestart > :from "
	}
	query += " order by created asc"
	_, err := dbmap.Select(&is, query, params)
	return is, err
}

// GetPhoto returns a pointer to a Photo, and an error (NotFoundError if no such row existed).
func GetPhoto(id int) (*Photo, error) {
	out, err := dbmap.Get(new(Photo), id)
	if err != nil {
		return nil, err
	} else if out == nil {
		return nil, errors.New(NotFoundError)
	}
	return out.(*Photo), err
}

// GetInvite returns a pointer to an Invite, and an error (NotFoundError if no such row existed).
func GetInvite(id int) (*Invite, error) {
	out, err := dbmap.Get(new(Invite), id)
	if err != nil {
		return nil, err
	} else if out == nil {
		return nil, errors.New(NotFoundError)
	}
	return out.(*Invite), err
}

// GetProfile returns a pointer to a Profile, and an error (NotFoundError if no such row existed).
func GetProfile(id int) (*Profile, error) {
	out, err := dbmap.Get(new(Profile), id)
	if err != nil {
		return nil, err
	} else if out == nil {
		return nil, errors.New(NotFoundError)
	}
	return out.(*Profile), err
}

// Get populates an Profile which is connected to the given Auth.
func (p *Profile) Get(a *Auth) error {
	err := dbmap.SelectOne(p, "select * from profile where id = $1", a.Profile)
	if err != nil {
		return err
	}
	p.Auth = a
	return nil
}

// Get populates an Auth as follows:
// if the Auth has a Token, get the Auth that matches that Token.
// if the Auth has a Username, get the Auth with that Username if the client hash matches.
// if the Auth has no Username, get the Auth by the SHA512 hash of the client hash.
func (a *Auth) Get() error {
	if a.Token != nil {
		return a.GetWithToken()
	} else if a.Username == nil {
		return a.GetWithHash()
	}
	return a.GetWithUsername()
}

// GetWithHash supports Auth.Get
func (a *Auth) GetWithHash() error {
	sum := sha512.Sum512([]byte(UsernamelessSalt + string(a.InHash)))
	hash := hex.EncodeToString(sum[0:64])
	return dbmap.SelectOne(a, "select * from auth where hash = $1", hash)
}

// GetWithToken supports Auth.Get
func (a *Auth) GetWithToken() error {
	return dbmap.SelectOne(a, "select * from auth where token = $1", a.Token)
}

// GetWithUsername supports Auth.Get
func (a *Auth) GetWithUsername() error {
	inHash := a.InHash
	err := dbmap.SelectOne(a, "select * from auth where username = $1", a.Username)
	if err != nil {
		return err
	}
	a.InHash = inHash // sigh: https://github.com/coopernurse/gorp/issues/164
	return nil
}

// Authenticated checks whether an Auth after Auth.Get is authenticated.
func (a *Auth) Authenticated() bool {
	if a.Username == nil {
		// If there's no username, then we're authenticated by default.
		return true
	}
	// If there is a username, then we have to check the hash;
	// no need to check a.InHash for existence, since any error is a fail.
	err := bcrypt.CompareHashAndPassword(a.Hash, a.InHash)
	return (err != nil)
}

// Login creates a logged in token and puts it in the given Auth.
// It does NOT authenticate; this is the step after that.
func (a *Auth) Login() (string, error) {
	t := token()
	a.Token = &t
	now := time.Now()
	a.LastAuth = &now
	a.Updated = &now
	count, err := dbmap.Update(a)
	if err != nil {
		return "", err
	} else if count != 1 {
		return "", errors.New("login Auth didn't update 1 row? count: " + string(count))
	}
	return t, nil
}

// Logout removes the logged in token from the given Auth.
func (a *Auth) Logout() error {
	a.Token = nil
	now := time.Now()
	a.Updated = &now
	count, err := dbmap.Update(a)
	if err != nil {
		return err
	} else if count != 1 {
		return errors.New("logout Auth didn't update 1 row? count: " + string(count))
	}
	return nil
}
