-- Aug 30, 2014

create table free_flag (
 flag integer not null references flag (id),
 free integer not null references free (id)
);

create table free_utype (
 utype integer not null references utype (id),
 free integer not null references free (id)
);

alter table free add column location point;
-- no one using locations, yet, so we don't have to migrate them
alter table profile drop column latitude;
alter table profile drop column longitude;


