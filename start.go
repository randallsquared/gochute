package main

import (
	"errors"
	"fmt"
	"log"
	"net/http"

	"github.com/randallsquared/go-tigertonic"
	"github.com/randallsquared/gochute/profile"
)

var (
	listen            string = ":1600"
	mux               *tigertonic.TrieServeMux
	cors              *tigertonic.CORSBuilder
	allowedPhotoTypes map[string]bool
)

// these "constants" are more properly config variables
const (
	ChuteToken       = "X-chute-token"
	UsernamelessSalt = "nx7sn3ks67La72&2"
)

type Response interface {
}

type Context struct {
	Auth    *profile.Auth
	Profile *profile.Profile
}

func error400(e string, addl ...interface{}) (int, http.Header, Response, error) {
	return abort(tigertonic.BadRequest{errors.New(e)}, e, addl)
}

func error401(e string, addl ...interface{}) (int, http.Header, Response, error) {
	return abort(tigertonic.Unauthorized{errors.New(e)}, e, addl)
}

func error403(e string, addl ...interface{}) (int, http.Header, Response, error) {
	return abort(tigertonic.Forbidden{errors.New(e)}, e, addl)
}

func error404(e string, addl ...interface{}) (int, http.Header, Response, error) {
	return abort(tigertonic.NotFound{errors.New(e)}, e, addl)
}

func error500(e string, addl ...interface{}) (int, http.Header, Response, error) {
	return abort(tigertonic.InternalServerError{errors.New(e)}, e, addl)
}

func abort(err error, why string, addl []interface{}) (int, http.Header, Response, error) {
	logged := []interface{}{}
	logged = append(logged, why)
	for _, val := range addl {
		logged = append(logged, val)
	}
	log.Println(logged...)
	return 0, nil, nil, err
}

func unauthenticated(h interface{}) http.Handler {
	return cors.Build(tigertonic.Marshaled(h)) //tigertonic.Logged(tigertonic.Marshaled(h), nil))
}

func authenticated(h interface{}) http.Handler {
	return cors.Build(tigertonic.If(authenticate, tigertonic.Marshaled(h)))
}

func rawAuthenticated(h http.Handler) http.Handler {
	return cors.Build(tigertonic.If(authenticate, h))
}

func init() {
	allowedPhotoTypes = map[string]bool{"image/jpg": true,
		"image/jpeg": true,
		"image/gif":  true,
		"image/png":  true}
	// set up web handlers
	cors = tigertonic.NewCORSBuilder()
	cors.AddAllowedOrigins("*")
	cors.AddAllowedHeaders("content-type", "cache-control", "pragma", ChuteToken)
	cors.AddExposedHeaders(ChuteToken)
	mux = tigertonic.NewTrieServeMux()
	mux.Handle("POST", "/profiles/self", unauthenticated(createProfile))
	mux.Handle("POST", "/actions/login", unauthenticated(login))
	mux.Handle("POST", "/actions/logout", authenticated(logout)) // er, why did I build this?
	mux.Handle("POST", "/auths", authenticated(connectAuth))
	mux.Handle("GET", "/profiles/self/auths", authenticated(getAuths))
	mux.Handle("POST", "/profiles/self/auths", authenticated(createAuth))
	mux.Handle("PUT", "/profiles/self/auths/{id}", authenticated(updateAuth))
	mux.Handle("GET", "/profiles/{id}", authenticated(getProfile))
	mux.Handle("GET", "/profiles/self", authenticated(getProfile))
	mux.Handle("PUT", "/profiles/self", authenticated(updateProfile))
	mux.Handle("POST", "/profiles/self/photos", rawAuthenticated(PhotoHandler{}))
	mux.Handle("DELETE", "/profiles/self/photos/{id}", authenticated(removePhoto))
	mux.Handle("PUT", "/profiles/self/photos/{id}", authenticated(updatePhoto))
	mux.Handle("POST", "/profiles/self/frees", authenticated(createFreetime))
	mux.Handle("GET", "/profiles/self/frees", authenticated(getFreetime))
	mux.Handle("GET", "/profiles/{id}/frees", authenticated(getFreetime))
	mux.Handle("DELETE", "/profiles/self/frees", authenticated(removeAllFreetime))
	mux.Handle("DELETE", "/profiles/self/frees/{start}", authenticated(removeFreetime))
	mux.Handle("GET", "/flags", unauthenticated(getFlags))
	mux.Handle("GET", "/types", unauthenticated(getTypes))
	mux.Handle("GET", "/rates", unauthenticated(getRates))
	// TODO: need to make sure this doesn't get cached
	mux.Handle("GET", "/profiles", authenticated(getProfilesBySearch))
	mux.Handle("POST", "/invites", authenticated(invite))
	mux.Handle("GET", "/invites/{id}", authenticated(getInvite))
	mux.Handle("GET", "/profiles/self/invites", authenticated(getMyInvitesBySearch))
	mux.Handle("POST", "/profiles/self/invites/{id}/status", authenticated(changeStatus))
	mux.Handle("POST", "/invites/{id}/messages", authenticated(addMessage))
	mux.Handle("DELETE", "/invites/{id}", authenticated(cancelInvite))
	mux.Handle("POST", "/invites/{id}/attendees", authenticated(addAttendees))

	/*
		       // we don't need this because we're returning signed URLs for the photos.
			   get("/profiles/{profileid}/photos/{photoid}", getPhoto)
		       // maybe later
			   put("/profiles/self/blocked/{id}", blockProfile)
			   del("/profiles/self/blocked/{id}", unBlockProfile)
	*/
}

func main() {
	handler := tigertonic.WithContext(mux, Context{})
	server := tigertonic.NewServer(listen, handler)
	err := server.ListenAndServe()
	if nil != err {
		fmt.Println("ERROR!")
	}
}
