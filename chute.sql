
-- use platform+uuid or profilename+password for authentication
--  if no platform or no uuid (web, for example), require profilename+password; profilename need not be unique!   We find the profile by the combination.
--  if platform and uuid, make a hash with some salt and display this upon request so that you can move to a new phone

-- authentication just produces an access token, which is then used to identify a profile until it expires.

--  "How should we identify you?  Use this device.  Username and password." 

-- A given profile can only have one profilename+password, but may have many devices.
--  If there's a profilename+password, some or all devices may be autologin=false. 
--  This will be presented as "Authorize this device" and 
--  "Remove all authorizations" buttons. 

-- In our scheme, a profilename+password is a floating "device" which has a default name of "web".

create table ratetype (
 id integer primary key,
 sort integer not null unique,
 name text,
 comm text
);

create table profile (
 id serial primary key,
 ratetype integer references ratetype (id) default 1,
 hourly integer,
 daily integer,
 created timestamp with time zone,
 updated timestamp with time zone,
 email text,
 phone text,
 name text,
 folder text
); 

create table log (
 id serial primary key,
 profile integer references profile (id),
 happened timestamp with time zone,
 event text
);

create table auth (
 id serial primary key,
 hash text not null unique,
 created timestamp with time zone,
 updated timestamp with time zone,
 lastAuth timestamp with time zone,
 profile integer not null references profile (id),
 name text not null, -- editable by user
 username text null unique,
 token text null unique,
 authorized boolean default false
);

create table photo (
 id serial primary key,
 profile integer not null references profile (id),
 created timestamp with time zone,
 href text not null,
 caption text
);

create table free (
 id serial primary key,
 profile integer not null references profile (id),
 location point,
 freestart timestamp with time zone not null,
 freeend timestamp with time zone not null,
 created timestamp with time zone not null,
 updated timestamp with time zone not null default now(),
 constraint profilestart unique (profile, freestart)
);

create table utype (
 id serial primary key,
 name varchar(127)
);

create table profile_utype (
 utype integer not null references utype (id),
 profile integer not null references profile (id)
);

create table free_utype (
 utype integer not null references utype (id),
 free integer not null references free (id) on delete cascade
);

create table flag (
 id serial primary key,
 name varchar(127)
);

create table profile_flag (
 flag integer not null references flag (id),
 profile integer not null references profile (id)
);

create table free_flag (
 flag integer not null references flag (id),
 free integer not null references free (id) on delete cascade
);

create table invite (
 id serial primary key,
 organizer integer not null references profile (id),
 active boolean default true,
 invitestart timestamp with time zone not null,
 inviteend timestamp with time zone,
 created timestamp with time zone not null,
 place text not null
);

create table profile_invite (
 profile integer not null references profile (id),
 invite integer not null references invite (id),
 status varchar(40) not null
);

create table message (
 id serial primary key,
 sent timestamp with time zone,
 sender integer not null references profile (id),
 invite integer not null references invite (id),
 body text not null,
 photo integer null references photo (id)
);

insert into utype (name) values ('Model'), ('Photographer'), ('Makeup Artist');
insert into flag (name) values ('Nude');



-- not yet converted

create table block (
 blocker integer not null references profile (id),
 blocked integer not null references profile (id)
);





