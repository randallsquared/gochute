 -- Sept 24, 2014

create table ratetype (
 id integer primary key,
 sort integer not null unique,
 name text,
 comm text
);

insert into ratetype values 
 (1, 1, 'Open', 'Open to discussion about rates'),
 (2, 2, 'Time for prints', 'Willing to shoot if provided with resulting work and rights thereto'),
 (3, 3, 'Depends on shoot', 'Rates depend on type, distance, and other characteristics of shoot'),
 (4, 4, 'Paid only', 'Paid shoots only');

alter table profile add column ratetype integer references ratetype (id) default 1;
alter table profile add column hourly integer;
alter table profile add column daily integer;

