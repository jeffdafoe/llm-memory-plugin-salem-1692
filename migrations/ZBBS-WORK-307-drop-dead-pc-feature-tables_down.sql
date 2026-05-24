-- ZBBS-WORK-307 rollback: recreate the sealed_note + npc_errand_offer tables
-- exactly as they were in the schema.sql baseline (table + sequence + default +
-- primary key + partial indexes + foreign keys). Manual rollback only — the
-- deploy runner applies *_up.sql, not *_down.sql. The tables come back empty
-- (they never had an INSERT path).

BEGIN;

-- sealed_note ---------------------------------------------------------------
CREATE TABLE public.sealed_note (
    id bigint NOT NULL,
    author_actor_id uuid NOT NULL,
    recipient_actor_id uuid NOT NULL,
    courier_actor_id uuid,
    body_text text NOT NULL,
    sealed boolean DEFAULT true NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    delivered_at timestamp with time zone,
    CONSTRAINT sealed_note_body_text_check CHECK ((length(TRIM(BOTH FROM body_text)) > 0))
);

CREATE SEQUENCE public.sealed_note_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;
ALTER SEQUENCE public.sealed_note_id_seq OWNED BY public.sealed_note.id;
ALTER TABLE ONLY public.sealed_note ALTER COLUMN id SET DEFAULT nextval('public.sealed_note_id_seq'::regclass);

ALTER TABLE ONLY public.sealed_note
    ADD CONSTRAINT sealed_note_pkey PRIMARY KEY (id);

CREATE INDEX ix_sealed_note_courier ON public.sealed_note USING btree (courier_actor_id) WHERE (sealed = true);
CREATE INDEX ix_sealed_note_delivered ON public.sealed_note USING btree (recipient_actor_id, delivered_at DESC) WHERE (sealed = false);

ALTER TABLE ONLY public.sealed_note
    ADD CONSTRAINT sealed_note_author_actor_id_fkey FOREIGN KEY (author_actor_id) REFERENCES public.actor(id) ON DELETE CASCADE;
ALTER TABLE ONLY public.sealed_note
    ADD CONSTRAINT sealed_note_courier_actor_id_fkey FOREIGN KEY (courier_actor_id) REFERENCES public.actor(id) ON DELETE SET NULL;
ALTER TABLE ONLY public.sealed_note
    ADD CONSTRAINT sealed_note_recipient_actor_id_fkey FOREIGN KEY (recipient_actor_id) REFERENCES public.actor(id) ON DELETE CASCADE;

-- npc_errand_offer ----------------------------------------------------------
CREATE TABLE public.npc_errand_offer (
    id bigint NOT NULL,
    requester_actor_id uuid NOT NULL,
    target_pc_actor_id uuid,
    fetch_item_kind character varying(32) NOT NULL,
    fetch_qty integer DEFAULT 1 NOT NULL,
    source_actor_id uuid,
    source_structure_id uuid,
    reward_coins integer NOT NULL,
    state character varying(16) NOT NULL,
    offered_at timestamp with time zone DEFAULT now() NOT NULL,
    accepted_at timestamp with time zone,
    completed_at timestamp with time zone,
    expires_at timestamp with time zone,
    CONSTRAINT npc_errand_offer_fetch_qty_check CHECK ((fetch_qty > 0)),
    CONSTRAINT npc_errand_offer_reward_coins_check CHECK ((reward_coins > 0)),
    CONSTRAINT npc_errand_offer_state_check CHECK (((state)::text = ANY ((ARRAY['offered'::character varying, 'accepted'::character varying, 'completed'::character varying, 'expired'::character varying, 'rejected'::character varying])::text[])))
);

CREATE SEQUENCE public.npc_errand_offer_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;
ALTER SEQUENCE public.npc_errand_offer_id_seq OWNED BY public.npc_errand_offer.id;
ALTER TABLE ONLY public.npc_errand_offer ALTER COLUMN id SET DEFAULT nextval('public.npc_errand_offer_id_seq'::regclass);

ALTER TABLE ONLY public.npc_errand_offer
    ADD CONSTRAINT npc_errand_offer_pkey PRIMARY KEY (id);

CREATE INDEX ix_npc_errand_offer_target_active ON public.npc_errand_offer USING btree (target_pc_actor_id) WHERE ((state)::text = ANY ((ARRAY['offered'::character varying, 'accepted'::character varying])::text[]));

ALTER TABLE ONLY public.npc_errand_offer
    ADD CONSTRAINT npc_errand_offer_fetch_item_kind_fkey FOREIGN KEY (fetch_item_kind) REFERENCES public.item_kind(name) ON UPDATE CASCADE;
ALTER TABLE ONLY public.npc_errand_offer
    ADD CONSTRAINT npc_errand_offer_requester_actor_id_fkey FOREIGN KEY (requester_actor_id) REFERENCES public.actor(id) ON DELETE CASCADE;
ALTER TABLE ONLY public.npc_errand_offer
    ADD CONSTRAINT npc_errand_offer_source_actor_id_fkey FOREIGN KEY (source_actor_id) REFERENCES public.actor(id) ON DELETE SET NULL;
ALTER TABLE ONLY public.npc_errand_offer
    ADD CONSTRAINT npc_errand_offer_source_structure_id_fkey FOREIGN KEY (source_structure_id) REFERENCES public.village_object(id) ON DELETE SET NULL;
ALTER TABLE ONLY public.npc_errand_offer
    ADD CONSTRAINT npc_errand_offer_target_pc_actor_id_fkey FOREIGN KEY (target_pc_actor_id) REFERENCES public.actor(id) ON DELETE SET NULL;

COMMIT;
