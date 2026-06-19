-- LLM-30 rollback: recreate the ten dropped tables exactly as they were in
-- the schema.sql baseline (tables + sequences + defaults + primary keys +
-- indexes + foreign keys), generated from a prod pg_dump of the live tables.
--
-- Manual rollback only -- the deploy runner applies *_up.sql, not *_down.sql.
-- The tables come back EMPTY; the dropped row data (v1 atmosphere/chronicler
-- history, etc.) is recoverable only from the VPS nightly backup. The custom
-- enum types these tables reference (chronicler_phase, concern_source_kind,
-- concern_target_kind, event_scope) are NOT dropped by the _up migration, so
-- they still exist for these CREATE TABLEs to bind against.

BEGIN;

CREATE TABLE public.actor_buy_state (
    actor_id uuid NOT NULL,
    item_kind character varying(32) NOT NULL,
    last_bought_from uuid,
    last_buy_succeeded_at timestamp with time zone,
    last_buy_failed_at timestamp with time zone,
    last_buy_failed_reason text
);

CREATE TABLE public.actor_delivery_in_progress (
    actor_id uuid NOT NULL,
    customer_id uuid NOT NULL,
    item_kind character varying(32) NOT NULL,
    qty smallint NOT NULL,
    pay_ledger_id bigint NOT NULL,
    customer_structure_id uuid NOT NULL,
    home_x double precision NOT NULL,
    home_y double precision NOT NULL,
    phase character varying(16) NOT NULL,
    started_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT actor_delivery_in_progress_phase_check CHECK (((phase)::text = ANY ((ARRAY['outbound'::character varying, 'inbound'::character varying])::text[]))),
    CONSTRAINT actor_delivery_in_progress_qty_check CHECK ((qty > 0))
);

CREATE TABLE public.actor_restock_in_progress (
    actor_id uuid NOT NULL,
    seller_id uuid NOT NULL,
    item_kind character varying(32) NOT NULL,
    seller_structure_id uuid NOT NULL,
    home_x double precision NOT NULL,
    home_y double precision NOT NULL,
    phase character varying(16) NOT NULL,
    started_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT actor_restock_in_progress_phase_check CHECK (((phase)::text = ANY ((ARRAY['outbound'::character varying, 'inbound'::character varying])::text[])))
);

CREATE TABLE public.gatherable_node (
    id bigint NOT NULL,
    x integer NOT NULL,
    y integer NOT NULL,
    item_kind character varying(32) NOT NULL,
    qty integer DEFAULT 1 NOT NULL,
    respawn_seconds integer DEFAULT 1800 NOT NULL,
    last_picked_at timestamp with time zone,
    display_label text,
    CONSTRAINT gatherable_node_qty_check CHECK ((qty > 0)),
    CONSTRAINT gatherable_node_respawn_seconds_check CHECK ((respawn_seconds > 0))
);

CREATE SEQUENCE public.gatherable_node_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE public.gatherable_node_id_seq OWNED BY public.gatherable_node.id;

CREATE TABLE public.town_crier_announcement (
    id bigint NOT NULL,
    text text NOT NULL,
    authored_at timestamp with time zone DEFAULT now() NOT NULL,
    expires_at timestamp with time zone,
    posted_count integer DEFAULT 0 NOT NULL,
    max_posts integer DEFAULT 3 NOT NULL,
    CONSTRAINT town_crier_announcement_max_posts_check CHECK ((max_posts > 0)),
    CONSTRAINT town_crier_announcement_posted_count_check CHECK ((posted_count >= 0)),
    CONSTRAINT town_crier_announcement_text_check CHECK ((length(TRIM(BOTH FROM text)) > 0))
);

CREATE SEQUENCE public.town_crier_announcement_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE public.town_crier_announcement_id_seq OWNED BY public.town_crier_announcement.id;

CREATE TABLE public.village_concern (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    source_kind public.concern_source_kind NOT NULL,
    source_id uuid NOT NULL,
    source_generation integer NOT NULL,
    target_kind public.concern_target_kind NOT NULL,
    target_id uuid NOT NULL,
    text text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE public.village_event (
    id bigint NOT NULL,
    event_type text NOT NULL,
    text text NOT NULL,
    actor_id uuid,
    structure_id uuid,
    x double precision,
    y double precision,
    occurred_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT village_event_event_type_check CHECK ((event_type = ANY (ARRAY['arrival'::text, 'departure'::text, 'phase_dawn'::text, 'phase_midday'::text, 'phase_dusk'::text, 'summon_ring'::text]))),
    CONSTRAINT village_event_xy_paired CHECK ((((x IS NULL) AND (y IS NULL)) OR ((x IS NOT NULL) AND (y IS NOT NULL))))
);

CREATE SEQUENCE public.village_event_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE public.village_event_id_seq OWNED BY public.village_event.id;

CREATE TABLE public.village_gossip (
    id bigint NOT NULL,
    text text NOT NULL,
    subject_actor_id uuid,
    authored_at timestamp with time zone DEFAULT now() NOT NULL,
    expires_at timestamp with time zone,
    region_scope text,
    CONSTRAINT village_gossip_text_check CHECK ((length(TRIM(BOTH FROM text)) > 0))
);

CREATE SEQUENCE public.village_gossip_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE public.village_gossip_id_seq OWNED BY public.village_gossip.id;

CREATE TABLE public.world_environment (
    id bigint NOT NULL,
    text text NOT NULL,
    set_by text DEFAULT 'salem-chronicler'::text NOT NULL,
    phase public.chronicler_phase,
    set_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE SEQUENCE public.world_environment_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE public.world_environment_id_seq OWNED BY public.world_environment.id;

CREATE TABLE public.world_events (
    id bigint NOT NULL,
    text text NOT NULL,
    scope_type public.event_scope DEFAULT 'village'::public.event_scope NOT NULL,
    scope_target text,
    set_by text DEFAULT 'salem-chronicler'::text NOT NULL,
    occurred_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE SEQUENCE public.world_events_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE public.world_events_id_seq OWNED BY public.world_events.id;

ALTER TABLE ONLY public.gatherable_node ALTER COLUMN id SET DEFAULT nextval('public.gatherable_node_id_seq'::regclass);

ALTER TABLE ONLY public.town_crier_announcement ALTER COLUMN id SET DEFAULT nextval('public.town_crier_announcement_id_seq'::regclass);

ALTER TABLE ONLY public.village_event ALTER COLUMN id SET DEFAULT nextval('public.village_event_id_seq'::regclass);

ALTER TABLE ONLY public.village_gossip ALTER COLUMN id SET DEFAULT nextval('public.village_gossip_id_seq'::regclass);

ALTER TABLE ONLY public.world_environment ALTER COLUMN id SET DEFAULT nextval('public.world_environment_id_seq'::regclass);

ALTER TABLE ONLY public.world_events ALTER COLUMN id SET DEFAULT nextval('public.world_events_id_seq'::regclass);

ALTER TABLE ONLY public.actor_buy_state
    ADD CONSTRAINT actor_buy_state_pkey PRIMARY KEY (actor_id, item_kind);

ALTER TABLE ONLY public.actor_delivery_in_progress
    ADD CONSTRAINT actor_delivery_in_progress_pkey PRIMARY KEY (actor_id);

ALTER TABLE ONLY public.actor_restock_in_progress
    ADD CONSTRAINT actor_restock_in_progress_pkey PRIMARY KEY (actor_id);

ALTER TABLE ONLY public.gatherable_node
    ADD CONSTRAINT gatherable_node_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.town_crier_announcement
    ADD CONSTRAINT town_crier_announcement_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.village_concern
    ADD CONSTRAINT village_concern_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.village_event
    ADD CONSTRAINT village_event_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.village_gossip
    ADD CONSTRAINT village_gossip_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.world_environment
    ADD CONSTRAINT world_environment_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.world_events
    ADD CONSTRAINT world_events_pkey PRIMARY KEY (id);

CREATE INDEX idx_actor_delivery_in_progress_customer_structure ON public.actor_delivery_in_progress USING btree (customer_structure_id);

CREATE UNIQUE INDEX idx_actor_delivery_in_progress_pay_ledger ON public.actor_delivery_in_progress USING btree (pay_ledger_id);

CREATE INDEX idx_actor_restock_in_progress_seller_structure ON public.actor_restock_in_progress USING btree (seller_structure_id);

CREATE INDEX ix_gatherable_node_xy ON public.gatherable_node USING btree (x, y);

CREATE INDEX ix_town_crier_announcement_active ON public.town_crier_announcement USING btree (authored_at, id) WHERE (posted_count < max_posts);

CREATE INDEX ix_village_concern_source ON public.village_concern USING btree (source_kind, source_id);

CREATE INDEX ix_village_concern_target ON public.village_concern USING btree (target_kind, target_id);

CREATE INDEX ix_village_gossip_recent ON public.village_gossip USING btree (authored_at DESC, id DESC);

CREATE INDEX ix_world_environment_set_at ON public.world_environment USING btree (set_at DESC);

CREATE INDEX ix_world_events_occurred_at ON public.world_events USING btree (occurred_at DESC);

CREATE INDEX village_event_recent_idx ON public.village_event USING btree (occurred_at DESC, id DESC);

ALTER TABLE ONLY public.actor_buy_state
    ADD CONSTRAINT actor_buy_state_actor_id_fkey FOREIGN KEY (actor_id) REFERENCES public.actor(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.actor_buy_state
    ADD CONSTRAINT actor_buy_state_item_kind_fkey FOREIGN KEY (item_kind) REFERENCES public.item_kind(name) ON UPDATE CASCADE ON DELETE CASCADE;

ALTER TABLE ONLY public.actor_buy_state
    ADD CONSTRAINT actor_buy_state_last_bought_from_fkey FOREIGN KEY (last_bought_from) REFERENCES public.actor(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.actor_delivery_in_progress
    ADD CONSTRAINT actor_delivery_in_progress_actor_id_fkey FOREIGN KEY (actor_id) REFERENCES public.actor(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.actor_delivery_in_progress
    ADD CONSTRAINT actor_delivery_in_progress_customer_id_fkey FOREIGN KEY (customer_id) REFERENCES public.actor(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.actor_delivery_in_progress
    ADD CONSTRAINT actor_delivery_in_progress_customer_structure_id_fkey FOREIGN KEY (customer_structure_id) REFERENCES public.village_object(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.actor_delivery_in_progress
    ADD CONSTRAINT actor_delivery_in_progress_item_kind_fkey FOREIGN KEY (item_kind) REFERENCES public.item_kind(name) ON UPDATE CASCADE;

ALTER TABLE ONLY public.actor_delivery_in_progress
    ADD CONSTRAINT actor_delivery_in_progress_pay_ledger_id_fkey FOREIGN KEY (pay_ledger_id) REFERENCES public.pay_ledger(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.actor_restock_in_progress
    ADD CONSTRAINT actor_restock_in_progress_actor_id_fkey FOREIGN KEY (actor_id) REFERENCES public.actor(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.actor_restock_in_progress
    ADD CONSTRAINT actor_restock_in_progress_item_kind_fkey FOREIGN KEY (item_kind) REFERENCES public.item_kind(name) ON UPDATE CASCADE;

ALTER TABLE ONLY public.actor_restock_in_progress
    ADD CONSTRAINT actor_restock_in_progress_seller_id_fkey FOREIGN KEY (seller_id) REFERENCES public.actor(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.actor_restock_in_progress
    ADD CONSTRAINT actor_restock_in_progress_seller_structure_id_fkey FOREIGN KEY (seller_structure_id) REFERENCES public.village_object(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.gatherable_node
    ADD CONSTRAINT gatherable_node_item_kind_fkey FOREIGN KEY (item_kind) REFERENCES public.item_kind(name) ON UPDATE CASCADE;

ALTER TABLE ONLY public.village_event
    ADD CONSTRAINT village_event_actor_id_fkey FOREIGN KEY (actor_id) REFERENCES public.actor(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.village_event
    ADD CONSTRAINT village_event_structure_id_fkey FOREIGN KEY (structure_id) REFERENCES public.village_object(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.village_gossip
    ADD CONSTRAINT village_gossip_subject_actor_id_fkey FOREIGN KEY (subject_actor_id) REFERENCES public.actor(id) ON DELETE SET NULL;

COMMIT;
