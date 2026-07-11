-- ============================================================================
-- Salem (zbbs) production schema baseline
--
-- Generated 2026-06-19 via:
--   pg_dump --schema-only --no-owner --no-privileges zbbs
-- Source: production VPS, database "zbbs" (the authoritative live copy).
--
-- WHY THIS FILE EXISTS:
-- This is the authoritative schema as it ACTUALLY EXISTS in production. The
-- fresh-install path (infrastructure/playbooks/deploy.yml) loads this baseline
-- first, then layers any NEWER *_up.sql migrations on top; the pg integration
-- harness (engine/sim/repo/pg/integration_test.go) does the same. On 2026-06-19
-- (LLM-43) the prior 2026-05-19 baseline plus its 24 post-baseline migrations
-- were folded into this fresh dump and those migration files deleted, so this
-- file alone now reproduces production.
--
-- Schema only -- no row data. Asset/NPC/catalog seed data is NOT included.
-- Regenerate by re-running the pg_dump command above against production, then
-- delete the now-folded *_up.sql / *_down.sql files.
-- ============================================================================

--
-- PostgreSQL database dump
--

\restrict b8FeRKxG7OGNF1plKr1MguXZbgfTr1amgP9YtJij9e6eUJyGemQbPVKUsMFaKF1

-- Dumped from database version 17.10 (Debian 17.10-0+deb13u1)
-- Dumped by pg_dump version 17.10 (Debian 17.10-0+deb13u1)

SET statement_timeout = 0;
SET lock_timeout = 0;
SET idle_in_transaction_session_timeout = 0;
SET transaction_timeout = 0;
SET client_encoding = 'UTF8';
SET standard_conforming_strings = on;
SELECT pg_catalog.set_config('search_path', '', false);
SET check_function_bodies = false;
SET xmloption = content;
SET client_min_messages = warning;
SET row_security = off;

--
-- Name: doors; Type: SCHEMA; Schema: -; Owner: -
--

CREATE SCHEMA doors;


--
-- Name: chronicler_phase; Type: TYPE; Schema: public; Owner: -
--

CREATE TYPE public.chronicler_phase AS ENUM (
    'dawn',
    'midday',
    'dusk'
);


--
-- Name: concern_source_kind; Type: TYPE; Schema: public; Owner: -
--

CREATE TYPE public.concern_source_kind AS ENUM (
    'village_object_content'
);


--
-- Name: concern_target_kind; Type: TYPE; Schema: public; Owner: -
--

CREATE TYPE public.concern_target_kind AS ENUM (
    'actor',
    'structure'
);


--
-- Name: event_scope; Type: TYPE; Schema: public; Owner: -
--

CREATE TYPE public.event_scope AS ENUM (
    'village',
    'local',
    'private'
);


--
-- Name: room_kind; Type: TYPE; Schema: public; Owner: -
--

CREATE TYPE public.room_kind AS ENUM (
    'common',
    'private',
    'staff'
);


SET default_tablespace = '';

SET default_table_access_method = heap;

--
-- Name: actor; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.actor (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    display_name character varying(100) NOT NULL,
    sprite_id uuid,
    current_x double precision NOT NULL,
    current_y double precision NOT NULL,
    facing character varying(5) DEFAULT 'south'::character varying NOT NULL,
    inside_structure_id text,
    current_huddle_id text,
    home_structure_id text,
    coins integer DEFAULT 20 NOT NULL,
    llm_memory_agent character varying(100),
    role character varying(50),
    work_structure_id text,
    schedule_start_minute smallint,
    schedule_end_minute smallint,
    last_agent_tick_at timestamp with time zone,
    login_username character varying(100),
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    last_seen_at timestamp with time zone DEFAULT now() NOT NULL,
    break_until timestamp with time zone,
    sleeping_until timestamp with time zone,
    inside_room_id bigint,
    snapshot_gen bigint DEFAULT 0 NOT NULL,
    move_attempt_counter bigint DEFAULT 0 NOT NULL,
    sim_state character varying(32) DEFAULT 'idle'::character varying NOT NULL,
    admin boolean DEFAULT false NOT NULL,
    move_destination jsonb,
    CONSTRAINT actor_driver_not_both CHECK ((NOT ((llm_memory_agent IS NOT NULL) AND (login_username IS NOT NULL)))),
    CONSTRAINT actor_facing_check CHECK (((facing)::text = ANY ((ARRAY['north'::character varying, 'south'::character varying, 'east'::character varying, 'west'::character varying])::text[]))),
    CONSTRAINT actor_schedule_end_minute_check CHECK (((schedule_end_minute IS NULL) OR ((schedule_end_minute >= 0) AND (schedule_end_minute <= 1439)))),
    CONSTRAINT actor_schedule_start_minute_check CHECK (((schedule_start_minute IS NULL) OR ((schedule_start_minute >= 0) AND (schedule_start_minute <= 1439)))),
    CONSTRAINT actor_schedule_window_all_or_none CHECK ((((schedule_start_minute IS NULL) AND (schedule_end_minute IS NULL)) OR ((schedule_start_minute IS NOT NULL) AND (schedule_end_minute IS NOT NULL))))
);


--
-- Name: actor_attribute; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.actor_attribute (
    actor_id uuid NOT NULL,
    slug character varying(64) NOT NULL,
    params jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    snapshot_gen bigint DEFAULT 0 NOT NULL
);


--
-- Name: actor_attribute_snapshot_gen_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.actor_attribute_snapshot_gen_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: actor_dwell_credit; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.actor_dwell_credit (
    actor_id uuid NOT NULL,
    object_id uuid NOT NULL,
    attribute character varying(32) NOT NULL,
    source character varying(16) NOT NULL,
    last_credited_at timestamp with time zone NOT NULL,
    remaining_ticks integer,
    dwell_delta smallint NOT NULL,
    dwell_period_minutes integer NOT NULL,
    snapshot_gen bigint DEFAULT 0 NOT NULL,
    CONSTRAINT actor_dwell_credit_dwell_delta_check CHECK ((dwell_delta < 0)),
    CONSTRAINT actor_dwell_credit_dwell_period_minutes_check CHECK ((dwell_period_minutes > 0)),
    CONSTRAINT actor_dwell_credit_remaining_matches_source CHECK (((((source)::text = 'item'::text) AND (remaining_ticks IS NOT NULL)) OR (((source)::text = 'object'::text) AND (remaining_ticks IS NULL)))),
    CONSTRAINT actor_dwell_credit_remaining_ticks_check CHECK (((remaining_ticks IS NULL) OR (remaining_ticks > 0))),
    CONSTRAINT actor_dwell_credit_source_check CHECK (((source)::text = ANY ((ARRAY['object'::character varying, 'item'::character varying])::text[])))
);


--
-- Name: actor_dwell_credit_snapshot_gen_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.actor_dwell_credit_snapshot_gen_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: actor_interaction_cooldown; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.actor_interaction_cooldown (
    speaker_id uuid NOT NULL,
    listener_id uuid NOT NULL,
    trigger character varying(16) NOT NULL,
    last_fired_at timestamp with time zone NOT NULL
);


--
-- Name: actor_inventory; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.actor_inventory (
    actor_id uuid NOT NULL,
    item_kind character varying(32) NOT NULL,
    quantity smallint NOT NULL,
    snapshot_gen bigint DEFAULT 0 NOT NULL,
    CONSTRAINT actor_inventory_quantity_check CHECK ((quantity > 0))
);


--
-- Name: actor_inventory_snapshot_gen_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.actor_inventory_snapshot_gen_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: actor_known_place; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.actor_known_place (
    actor_id uuid NOT NULL,
    place_ref uuid NOT NULL,
    place_kind text NOT NULL,
    affordances jsonb DEFAULT '[]'::jsonb NOT NULL,
    first_learned_at timestamp with time zone DEFAULT now() NOT NULL,
    last_experienced_at timestamp with time zone DEFAULT now() NOT NULL,
    snapshot_gen bigint DEFAULT 0 NOT NULL
);


--
-- Name: actor_known_place_snapshot_gen_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.actor_known_place_snapshot_gen_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: actor_narrative_state; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.actor_narrative_state (
    actor_id uuid NOT NULL,
    seed_text text DEFAULT ''::text NOT NULL,
    evolving_summary text DEFAULT ''::text NOT NULL,
    about_me text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    last_consolidated_at timestamp with time zone,
    snapshot_gen bigint DEFAULT 0 NOT NULL
);


--
-- Name: actor_narrative_state_snapshot_gen_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.actor_narrative_state_snapshot_gen_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: actor_need; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.actor_need (
    actor_id uuid NOT NULL,
    key character varying(32) NOT NULL,
    value smallint DEFAULT 0 NOT NULL,
    snapshot_gen bigint DEFAULT 0 NOT NULL,
    CONSTRAINT actor_need_key_check CHECK (((key)::text = ANY ((ARRAY['hunger'::character varying, 'thirst'::character varying, 'tiredness'::character varying])::text[]))),
    CONSTRAINT actor_need_value_check CHECK (((value >= 0) AND (value <= 24)))
);


--
-- Name: actor_need_snapshot_gen_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.actor_need_snapshot_gen_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: actor_produce_state; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.actor_produce_state (
    actor_id uuid NOT NULL,
    item_kind character varying(32) NOT NULL,
    last_produced_at timestamp with time zone,
    snapshot_gen bigint DEFAULT 0 NOT NULL
);


--
-- Name: actor_produce_state_snapshot_gen_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.actor_produce_state_snapshot_gen_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: actor_relationship; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.actor_relationship (
    actor_id uuid NOT NULL,
    other_actor_id uuid NOT NULL,
    summary_text text DEFAULT ''::text NOT NULL,
    salient_facts jsonb DEFAULT '[]'::jsonb NOT NULL,
    interaction_count integer DEFAULT 0 NOT NULL,
    last_interaction_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    last_consolidated_at timestamp with time zone,
    dropped_fact_count integer DEFAULT 0 NOT NULL,
    snapshot_gen bigint DEFAULT 0 NOT NULL,
    CONSTRAINT actor_relationship_no_self CHECK ((actor_id <> other_actor_id))
);


--
-- Name: actor_relationship_snapshot_gen_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.actor_relationship_snapshot_gen_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: actor_snapshot_gen_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.actor_snapshot_gen_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: agent_action_log; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.agent_action_log (
    id bigint NOT NULL,
    actor_id uuid,
    occurred_at timestamp with time zone DEFAULT now() NOT NULL,
    source text NOT NULL,
    action_type text NOT NULL,
    payload jsonb NOT NULL,
    result text NOT NULL,
    error text,
    speaker_name character varying(100) NOT NULL,
    huddle_id text,
    CONSTRAINT agent_action_log_result_check CHECK ((result = ANY (ARRAY['ok'::text, 'rejected'::text, 'failed'::text, 'declined'::text, 'countered'::text]))),
    CONSTRAINT agent_action_log_source_check CHECK ((source = ANY (ARRAY['agent'::text, 'magistrate'::text, 'player'::text, 'engine'::text])))
);


--
-- Name: agent_action_log_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.agent_action_log_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: agent_action_log_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.agent_action_log_id_seq OWNED BY public.agent_action_log.id;


--
-- Name: asset; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.asset (
    name character varying(100) NOT NULL,
    category character varying(30) NOT NULL,
    default_state character varying(30) DEFAULT 'default'::character varying NOT NULL,
    anchor_x double precision DEFAULT 0.5 NOT NULL,
    anchor_y double precision DEFAULT 0.85 NOT NULL,
    layer character varying(10) DEFAULT 'objects'::character varying NOT NULL,
    created_at timestamp(0) without time zone DEFAULT now() NOT NULL,
    pack_id character varying(60),
    interior boolean DEFAULT false NOT NULL,
    fits_slot character varying(32) DEFAULT NULL::character varying,
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    source_file character varying(200) DEFAULT NULL::character varying,
    rotation_algo character varying(32) DEFAULT 'random_per_object'::character varying NOT NULL,
    transition_spread_seconds integer DEFAULT 0 NOT NULL,
    is_obstacle boolean DEFAULT false NOT NULL,
    is_passage boolean DEFAULT false NOT NULL,
    z_index integer DEFAULT 10 NOT NULL,
    footprint_left integer DEFAULT 0 NOT NULL,
    footprint_right integer DEFAULT 0 NOT NULL,
    footprint_top integer DEFAULT 0 NOT NULL,
    footprint_bottom integer DEFAULT 0 NOT NULL,
    door_offset_x integer,
    door_offset_y integer,
    visible_when_inside boolean DEFAULT false NOT NULL,
    stand_offset_x integer,
    stand_offset_y integer,
    occupied_min_count integer DEFAULT 1 NOT NULL,
    occupied_night_only boolean DEFAULT false NOT NULL,
    CONSTRAINT asset_footprint_bottom_check CHECK ((footprint_bottom >= 0)),
    CONSTRAINT asset_footprint_left_check CHECK ((footprint_left >= 0)),
    CONSTRAINT asset_footprint_right_check CHECK ((footprint_right >= 0)),
    CONSTRAINT asset_footprint_top_check CHECK ((footprint_top >= 0)),
    CONSTRAINT asset_rotation_algo_check CHECK (((rotation_algo)::text = ANY ((ARRAY['random_per_object'::character varying, 'random_per_asset'::character varying, 'deterministic'::character varying])::text[]))),
    CONSTRAINT asset_transition_spread_seconds_check CHECK ((transition_spread_seconds >= 0)),
    CONSTRAINT chk_asset_occupied_min_count CHECK ((occupied_min_count >= 1))
);


--
-- Name: asset_slot; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.asset_slot (
    id integer NOT NULL,
    slot_name character varying(32) NOT NULL,
    offset_x integer DEFAULT 0 NOT NULL,
    offset_y integer DEFAULT 0 NOT NULL,
    asset_id uuid NOT NULL
);


--
-- Name: asset_slot_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.asset_slot_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: asset_slot_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.asset_slot_id_seq OWNED BY public.asset_slot.id;


--
-- Name: asset_state; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.asset_state (
    id integer NOT NULL,
    state character varying(30) NOT NULL,
    sheet character varying(200) NOT NULL,
    src_x integer NOT NULL,
    src_y integer NOT NULL,
    src_w integer NOT NULL,
    src_h integer NOT NULL,
    created_at timestamp(0) without time zone DEFAULT now() NOT NULL,
    frame_count integer DEFAULT 1 NOT NULL,
    frame_rate double precision DEFAULT 0 NOT NULL,
    asset_id uuid NOT NULL
);


--
-- Name: asset_state_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.asset_state_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: asset_state_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.asset_state_id_seq OWNED BY public.asset_state.id;


--
-- Name: asset_state_light; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.asset_state_light (
    state_id integer NOT NULL,
    color character varying(7) NOT NULL,
    radius integer NOT NULL,
    energy double precision DEFAULT 1.0 NOT NULL,
    offset_x integer DEFAULT 0 NOT NULL,
    offset_y integer DEFAULT 0 NOT NULL,
    flicker_amplitude double precision DEFAULT 0 NOT NULL,
    flicker_period_ms integer DEFAULT 0 NOT NULL
);


--
-- Name: asset_state_tag; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.asset_state_tag (
    state_id integer NOT NULL,
    tag character varying(50) NOT NULL
);


--
-- Name: attribute_definition; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.attribute_definition (
    slug character varying(64) NOT NULL,
    display_name character varying(100) NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    scope character varying(16) DEFAULT 'actor'::character varying NOT NULL,
    tools jsonb DEFAULT '[]'::jsonb NOT NULL,
    instructions text DEFAULT ''::text NOT NULL,
    behaviors jsonb DEFAULT '[]'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT attribute_definition_scope_check CHECK (((scope)::text = ANY ((ARRAY['actor'::character varying, 'object'::character varying, 'both'::character varying])::text[])))
);


--
-- Name: huddle_member; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.huddle_member (
    huddle_id text NOT NULL,
    actor_id text NOT NULL,
    snapshot_gen bigint DEFAULT 0 NOT NULL,
    CONSTRAINT huddle_member_actor_id_nonempty CHECK ((actor_id <> ''::text)),
    CONSTRAINT huddle_member_huddle_id_nonempty CHECK ((huddle_id <> ''::text))
);


--
-- Name: huddle_member_snapshot_gen_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.huddle_member_snapshot_gen_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: huddle_snapshot_gen_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.huddle_snapshot_gen_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: item_kind; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.item_kind (
    name character varying(32) NOT NULL,
    display_label character varying(64) NOT NULL,
    category character varying(32) NOT NULL,
    sort_order smallint DEFAULT 0 NOT NULL,
    capabilities text[] DEFAULT '{}'::text[] NOT NULL,
    hours_per_unit smallint,
    consume_dwell_narration text,
    display_label_singular character varying(64),
    display_label_plural character varying(64),
    CONSTRAINT item_kind_hours_per_unit_check CHECK (((hours_per_unit IS NULL) OR (hours_per_unit >= 0)))
);


--
-- Name: item_recipe; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.item_recipe (
    output_item character varying(32) NOT NULL,
    output_qty smallint DEFAULT 1 NOT NULL,
    rate_qty smallint NOT NULL,
    rate_per_hours smallint NOT NULL,
    inputs jsonb DEFAULT '[]'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    wholesale_price smallint,
    retail_price smallint,
    CONSTRAINT item_recipe_inputs_array CHECK ((jsonb_typeof(inputs) = 'array'::text)),
    CONSTRAINT item_recipe_output_qty_check CHECK ((output_qty > 0)),
    CONSTRAINT item_recipe_rate_per_hours_check CHECK ((rate_per_hours > 0)),
    CONSTRAINT item_recipe_rate_qty_check CHECK ((rate_qty > 0))
);


--
-- Name: item_satisfies; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.item_satisfies (
    item_kind character varying(32) NOT NULL,
    attribute character varying(32) NOT NULL,
    amount integer NOT NULL,
    dwell_amount integer,
    dwell_period_minutes integer,
    dwell_total_ticks integer,
    CONSTRAINT item_satisfies_amount_check CHECK ((amount > 0)),
    CONSTRAINT item_satisfies_dwell_amount_positive CHECK (((dwell_amount IS NULL) OR (dwell_amount > 0))),
    CONSTRAINT item_satisfies_dwell_period_positive CHECK (((dwell_period_minutes IS NULL) OR (dwell_period_minutes > 0))),
    CONSTRAINT item_satisfies_dwell_total_ticks_positive CHECK (((dwell_total_ticks IS NULL) OR (dwell_total_ticks > 0))),
    CONSTRAINT item_satisfies_dwell_triple CHECK ((((dwell_amount IS NULL) AND (dwell_period_minutes IS NULL) AND (dwell_total_ticks IS NULL)) OR ((dwell_amount IS NOT NULL) AND (dwell_period_minutes IS NOT NULL) AND (dwell_total_ticks IS NOT NULL))))
);


--
-- Name: messenger_messages; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.messenger_messages (
    id bigint NOT NULL,
    body text NOT NULL,
    headers text NOT NULL,
    queue_name character varying(190) NOT NULL,
    created_at timestamp(0) without time zone NOT NULL,
    available_at timestamp(0) without time zone NOT NULL,
    delivered_at timestamp(0) without time zone DEFAULT NULL::timestamp without time zone
);


--
-- Name: messenger_messages_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

ALTER TABLE public.messenger_messages ALTER COLUMN id ADD GENERATED BY DEFAULT AS IDENTITY (
    SEQUENCE NAME public.messenger_messages_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);


--
-- Name: migrations_applied; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.migrations_applied (
    migration_name character varying(255) NOT NULL,
    applied_at timestamp with time zone DEFAULT now()
);


--
-- Name: narration_pool_expansion; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.narration_pool_expansion (
    pool_key character varying(64) NOT NULL,
    phrase text NOT NULL,
    generated_by character varying(64) NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: npc_acquaintance; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.npc_acquaintance (
    actor_id uuid NOT NULL,
    other_name character varying(100) NOT NULL,
    first_interacted_at timestamp with time zone DEFAULT now() NOT NULL,
    snapshot_gen bigint DEFAULT 0 NOT NULL
);


--
-- Name: npc_acquaintance_snapshot_gen_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.npc_acquaintance_snapshot_gen_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: npc_sprite; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.npc_sprite (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    name character varying(100) NOT NULL,
    sheet character varying(255) NOT NULL,
    frame_width integer DEFAULT 32 NOT NULL,
    frame_height integer DEFAULT 32 NOT NULL,
    pack_id character varying(60)
);


--
-- Name: npc_sprite_animation; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.npc_sprite_animation (
    sprite_id uuid NOT NULL,
    direction character varying(5) NOT NULL,
    animation character varying(10) NOT NULL,
    row_index integer NOT NULL,
    frame_count integer NOT NULL,
    frame_rate double precision DEFAULT 6.0 NOT NULL,
    CONSTRAINT npc_sprite_animation_animation_check CHECK (((animation)::text = ANY ((ARRAY['idle'::character varying, 'walk'::character varying])::text[]))),
    CONSTRAINT npc_sprite_animation_direction_check CHECK (((direction)::text = ANY ((ARRAY['north'::character varying, 'south'::character varying, 'east'::character varying, 'west'::character varying])::text[])))
);


--
-- Name: object_refresh; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.object_refresh (
    object_id uuid NOT NULL,
    attribute character varying(32) NOT NULL,
    amount smallint NOT NULL,
    available_quantity smallint,
    max_quantity smallint,
    refresh_mode character varying(16) DEFAULT 'continuous'::character varying NOT NULL,
    refresh_period_hours integer,
    last_refresh_at timestamp with time zone,
    dwell_amount smallint,
    dwell_period_minutes integer,
    snapshot_gen bigint DEFAULT 0 NOT NULL,
    gather_item character varying(32),
    CONSTRAINT object_refresh_amount_negative CHECK ((amount < 0)),
    CONSTRAINT object_refresh_available_le_max CHECK (((available_quantity IS NULL) OR (available_quantity <= max_quantity))),
    CONSTRAINT object_refresh_dwell_amount_negative CHECK (((dwell_amount IS NULL) OR (dwell_amount < 0))),
    CONSTRAINT object_refresh_dwell_pair CHECK (((dwell_amount IS NULL) = (dwell_period_minutes IS NULL))),
    CONSTRAINT object_refresh_dwell_period_positive CHECK (((dwell_period_minutes IS NULL) OR (dwell_period_minutes > 0))),
    CONSTRAINT object_refresh_max_positive CHECK (((max_quantity IS NULL) OR (max_quantity > 0))),
    CONSTRAINT object_refresh_mode_check CHECK (((refresh_mode)::text = ANY ((ARRAY['continuous'::character varying, 'periodic'::character varying])::text[]))),
    CONSTRAINT object_refresh_period_positive CHECK (((refresh_period_hours IS NULL) OR (refresh_period_hours > 0))),
    CONSTRAINT object_refresh_quantity_nonneg CHECK (((available_quantity IS NULL) OR (available_quantity >= 0))),
    CONSTRAINT object_refresh_quantity_pair CHECK ((((available_quantity IS NULL) AND (max_quantity IS NULL)) OR ((available_quantity IS NOT NULL) AND (max_quantity IS NOT NULL))))
);


--
-- Name: object_refresh_snapshot_gen_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.object_refresh_snapshot_gen_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: pay_ledger; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.pay_ledger (
    id bigint NOT NULL,
    huddle_id text,
    scene_id uuid,
    buyer_id text NOT NULL,
    seller_id text NOT NULL,
    item_kind character varying(32),
    qty integer,
    offered_amount integer NOT NULL,
    quoted_unit_amount integer,
    consume_now boolean DEFAULT false NOT NULL,
    state character varying(16) NOT NULL,
    message text,
    counter_amount integer,
    parent_id bigint,
    depth integer DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    resolved_at timestamp with time zone,
    ready_by date NOT NULL,
    delivered_on timestamp with time zone,
    fulfillment_status character varying(16) NOT NULL,
    consumer_actor_ids text[],
    expires_at timestamp with time zone,
    CONSTRAINT pay_ledger_check CHECK ((((state)::text = 'pending'::text) = (resolved_at IS NULL))),
    CONSTRAINT pay_ledger_counter_amount_check CHECK (((counter_amount IS NULL) OR (counter_amount >= 0))),
    CONSTRAINT pay_ledger_depth_check CHECK ((depth >= 0)),
    CONSTRAINT pay_ledger_fulfillment_status_check CHECK (((fulfillment_status)::text = ANY ((ARRAY['pending'::character varying, 'ready'::character varying, 'delivered'::character varying, 'expired'::character varying])::text[]))),
    CONSTRAINT pay_ledger_offered_amount_check CHECK ((offered_amount >= 0)),
    CONSTRAINT pay_ledger_qty_check CHECK (((qty IS NULL) OR (qty > 0))),
    CONSTRAINT pay_ledger_quoted_unit_amount_check CHECK (((quoted_unit_amount IS NULL) OR (quoted_unit_amount >= 0))),
    CONSTRAINT pay_ledger_state_check CHECK (((state)::text = ANY ((ARRAY['pending'::character varying, 'accepted'::character varying, 'declined'::character varying, 'countered'::character varying, 'withdrawn'::character varying, 'failed'::character varying, 'no_stock'::character varying])::text[])))
);


--
-- Name: pay_ledger_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.pay_ledger_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: pay_ledger_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.pay_ledger_id_seq OWNED BY public.pay_ledger.id;


--
-- Name: refresh_attribute; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.refresh_attribute (
    name character varying(32) NOT NULL,
    display_label character varying(64) NOT NULL,
    sort_order smallint DEFAULT 0 NOT NULL
);


--
-- Name: room_access; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.room_access (
    room_id bigint NOT NULL,
    actor_id uuid NOT NULL,
    granted_via_ledger_id bigint,
    granted_at timestamp with time zone DEFAULT now() NOT NULL,
    expires_at timestamp with time zone,
    kind public.room_kind NOT NULL,
    active boolean DEFAULT true NOT NULL,
    snapshot_gen bigint DEFAULT 0 NOT NULL
);


--
-- Name: room_access_snapshot_gen_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.room_access_snapshot_gen_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: scene; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.scene (
    id text NOT NULL,
    origin_at timestamp with time zone NOT NULL,
    origin_kind text NOT NULL,
    bound_kind text NOT NULL,
    bound_structure_id text,
    bound_anchor_x integer,
    bound_anchor_y integer,
    bound_radius integer,
    origin_position_x integer NOT NULL,
    origin_position_y integer NOT NULL,
    snapshot_gen bigint DEFAULT 0 NOT NULL,
    CONSTRAINT scene_bound_shape_valid CHECK ((((bound_kind = 'structure'::text) AND (bound_structure_id IS NOT NULL) AND (bound_anchor_x IS NULL) AND (bound_anchor_y IS NULL) AND (bound_radius IS NULL)) OR ((bound_kind = 'area'::text) AND (bound_structure_id IS NULL) AND (bound_anchor_x IS NOT NULL) AND (bound_anchor_y IS NOT NULL) AND (bound_radius IS NOT NULL) AND (bound_radius >= 0)))),
    CONSTRAINT scene_bound_structure_id_nonempty CHECK (((bound_structure_id IS NULL) OR (btrim(bound_structure_id) <> ''::text))),
    CONSTRAINT scene_id_nonempty CHECK ((btrim(id) <> ''::text)),
    CONSTRAINT scene_origin_kind_nonempty CHECK ((btrim(origin_kind) <> ''::text))
);


--
-- Name: scene_huddle; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.scene_huddle (
    id text NOT NULL,
    structure_id text,
    started_at timestamp with time zone DEFAULT now() NOT NULL,
    concluded_at timestamp with time zone,
    snapshot_gen bigint DEFAULT 0 NOT NULL,
    CONSTRAINT scene_huddle_id_format CHECK ((id ~ '^hud-[0-9a-f]{32}$'::text)),
    CONSTRAINT scene_huddle_id_nonempty CHECK ((id <> ''::text)),
    CONSTRAINT scene_huddle_structure_id_nonempty CHECK (((structure_id IS NULL) OR (structure_id <> ''::text)))
);


--
-- Name: scene_huddle_ref; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.scene_huddle_ref (
    scene_id text NOT NULL,
    huddle_id text NOT NULL,
    snapshot_gen bigint DEFAULT 0 NOT NULL,
    CONSTRAINT scene_huddle_ref_huddle_id_nonempty CHECK ((btrim(huddle_id) <> ''::text)),
    CONSTRAINT scene_huddle_ref_scene_id_nonempty CHECK ((btrim(scene_id) <> ''::text))
);


--
-- Name: scene_huddle_ref_snapshot_gen_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.scene_huddle_ref_snapshot_gen_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: scene_quote; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.scene_quote (
    huddle_id text NOT NULL,
    from_actor_id uuid NOT NULL,
    item_kind character varying(32) NOT NULL,
    unit_price integer NOT NULL,
    quoted_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT scene_quote_unit_price_check CHECK ((unit_price >= 0))
);


--
-- Name: scene_snapshot_gen_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.scene_snapshot_gen_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: scenes; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.scenes (
    scene_id uuid NOT NULL,
    structure_id uuid,
    started_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: setting; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.setting (
    key character varying(100) NOT NULL,
    value text,
    description text DEFAULT NULL::character varying,
    is_public boolean DEFAULT false NOT NULL
);


--
-- Name: structure; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.structure (
    id text NOT NULL,
    display_name text NOT NULL,
    tags text[] DEFAULT '{}'::text[] NOT NULL,
    leads_to_realm text DEFAULT ''::text NOT NULL,
    snapshot_gen bigint DEFAULT 0 NOT NULL,
    CONSTRAINT structure_display_name_nonempty CHECK ((btrim(display_name) <> ''::text)),
    CONSTRAINT structure_id_nonempty CHECK ((btrim(id) <> ''::text))
);


--
-- Name: structure_room; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.structure_room (
    id bigint NOT NULL,
    structure_id text NOT NULL,
    name text NOT NULL,
    kind public.room_kind NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    snapshot_gen bigint DEFAULT 0 NOT NULL,
    CONSTRAINT structure_room_id_positive CHECK ((id > 0)),
    CONSTRAINT structure_room_name_nonempty CHECK ((btrim(name) <> ''::text)),
    CONSTRAINT structure_room_structure_id_nonempty CHECK ((btrim(structure_id) <> ''::text))
);


--
-- Name: structure_room_snapshot_gen_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.structure_room_snapshot_gen_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: structure_snapshot_gen_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.structure_snapshot_gen_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: structure_subspace_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.structure_subspace_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: structure_subspace_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.structure_subspace_id_seq OWNED BY public.structure_room.id;


--
-- Name: tileset_pack; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.tileset_pack (
    id character varying(60) NOT NULL,
    name character varying(100) NOT NULL,
    url character varying(500),
    created_at timestamp(0) without time zone DEFAULT now() NOT NULL,
    pack_group character varying(60),
    pack_source character varying(200)
);


--
-- Name: user; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public."user" (
    id uuid NOT NULL,
    username character varying(35) NOT NULL,
    email character varying(180),
    roles json NOT NULL,
    password character varying(255) NOT NULL,
    created_at timestamp(0) without time zone NOT NULL,
    last_login_at timestamp(0) without time zone DEFAULT NULL::timestamp without time zone,
    is_active boolean NOT NULL
);


--
-- Name: user_profile; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.user_profile (
    id uuid NOT NULL,
    alias character varying(35) DEFAULT NULL::character varying,
    gender character varying(10) NOT NULL,
    entry_message text,
    exit_message text,
    bio text,
    avatar_url character varying(255) DEFAULT NULL::character varying,
    preferred_color character varying(1) DEFAULT NULL::character varying,
    timezone character varying(50) DEFAULT NULL::character varying,
    created_at timestamp(0) without time zone NOT NULL,
    updated_at timestamp(0) without time zone NOT NULL,
    user_id uuid NOT NULL
);


--
-- Name: village_object; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.village_object (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    x double precision NOT NULL,
    y double precision NOT NULL,
    placed_by character varying(100),
    owner character varying(100),
    created_at timestamp(0) without time zone DEFAULT now() NOT NULL,
    current_state character varying(30) DEFAULT 'default'::character varying NOT NULL,
    display_name character varying(100),
    attached_to uuid,
    asset_id uuid,
    loiter_offset_x integer,
    loiter_offset_y integer,
    entry_policy text DEFAULT 'closed'::text NOT NULL,
    content_generation integer DEFAULT 0 NOT NULL,
    snapshot_gen bigint DEFAULT 0 NOT NULL,
    available_quantity integer DEFAULT 0 NOT NULL,
    tags text[] DEFAULT '{}'::text[] NOT NULL,
    owner_actor_id text,
    CONSTRAINT village_object_entry_policy_check CHECK ((entry_policy = ANY (ARRAY[''::text, 'open'::text, 'owner-only'::text, 'closed'::text]))),
    CONSTRAINT village_object_tags_no_nulls CHECK ((array_position(tags, NULL::text) IS NULL))
);


--
-- Name: village_object_snapshot_gen_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.village_object_snapshot_gen_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: village_terrain; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.village_terrain (
    id integer DEFAULT 1 NOT NULL,
    width integer NOT NULL,
    height integer NOT NULL,
    data bytea NOT NULL,
    updated_by character varying(100),
    updated_at timestamp(0) without time zone DEFAULT now() NOT NULL,
    CONSTRAINT single_terrain CHECK ((id = 1))
);


--
-- Name: world_state; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.world_state (
    id integer DEFAULT 1 NOT NULL,
    phase character varying(20) NOT NULL,
    last_transition_at timestamp with time zone DEFAULT now() NOT NULL,
    last_rotation_at timestamp with time zone DEFAULT now() NOT NULL,
    weather text DEFAULT ''::text NOT NULL,
    atmosphere text DEFAULT ''::text NOT NULL,
    last_needs_tick_at timestamp with time zone,
    CONSTRAINT world_phase_phase_check CHECK (((phase)::text = ANY ((ARRAY['day'::character varying, 'night'::character varying])::text[]))),
    CONSTRAINT world_state_singleton CHECK ((id = 1))
);


--
-- Name: zchat_action; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.zchat_action (
    id uuid NOT NULL,
    slug character varying(30) NOT NULL,
    action_list_slug character varying(50) NOT NULL,
    template_self character varying(255) NOT NULL,
    template_target character varying(255) NOT NULL,
    template_no_target character varying(255) NOT NULL,
    is_active boolean NOT NULL,
    sort_order integer NOT NULL
);


--
-- Name: zchat_invite; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.zchat_invite (
    id uuid NOT NULL,
    expires_at timestamp(0) without time zone NOT NULL,
    is_accepted boolean,
    created_at timestamp(0) without time zone NOT NULL,
    room_id uuid NOT NULL,
    invited_user_id uuid NOT NULL,
    invited_by_id uuid NOT NULL
);


--
-- Name: zchat_message; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.zchat_message (
    id uuid NOT NULL,
    message_type character varying(20) NOT NULL,
    content text NOT NULL,
    metadata json,
    is_deleted boolean NOT NULL,
    created_at timestamp(0) without time zone NOT NULL,
    room_id uuid NOT NULL,
    sender_id uuid NOT NULL,
    target_user_id uuid
);


--
-- Name: zchat_presence; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.zchat_presence (
    id uuid NOT NULL,
    status character varying(20) NOT NULL,
    display_alias character varying(35) DEFAULT NULL::character varying,
    is_invisible boolean NOT NULL,
    last_activity_at timestamp(0) without time zone NOT NULL,
    connected_at timestamp(0) without time zone NOT NULL,
    mercure_connection_id character varying(255) DEFAULT NULL::character varying,
    user_id uuid NOT NULL,
    room_id uuid
);


--
-- Name: zchat_private_message; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.zchat_private_message (
    id uuid NOT NULL,
    content text NOT NULL,
    is_read boolean NOT NULL,
    read_at timestamp(0) without time zone DEFAULT NULL::timestamp without time zone,
    is_deleted_by_sender boolean NOT NULL,
    is_deleted_by_recipient boolean NOT NULL,
    created_at timestamp(0) without time zone NOT NULL,
    sender_id uuid NOT NULL,
    recipient_id uuid NOT NULL
);


--
-- Name: zchat_room; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.zchat_room (
    id uuid NOT NULL,
    slug character varying(50) NOT NULL,
    name character varying(50) NOT NULL,
    description text,
    room_type character varying(20) NOT NULL,
    min_security_level integer NOT NULL,
    max_security_level integer NOT NULL,
    min_age integer,
    max_age integer,
    no_access_can_see boolean NOT NULL,
    action_list_slug character varying(50) NOT NULL,
    max_users integer,
    is_active boolean NOT NULL,
    created_at timestamp(0) without time zone NOT NULL,
    updated_at timestamp(0) without time zone NOT NULL,
    room_leader_id uuid
);


--
-- Name: zchat_squelch; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.zchat_squelch (
    id uuid NOT NULL,
    created_at timestamp(0) without time zone NOT NULL,
    user_id uuid NOT NULL,
    squelched_user_id uuid NOT NULL
);


--
-- Name: zchat_trivia_game; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.zchat_trivia_game (
    id uuid NOT NULL,
    status character varying(20) NOT NULL,
    current_round integer NOT NULL,
    max_rounds integer NOT NULL,
    question_started_at timestamp(0) without time zone DEFAULT NULL::timestamp without time zone,
    question_timeout integer NOT NULL,
    created_at timestamp(0) without time zone NOT NULL,
    ended_at timestamp(0) without time zone DEFAULT NULL::timestamp without time zone,
    room_id uuid NOT NULL,
    current_question_id uuid
);


--
-- Name: zchat_trivia_player; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.zchat_trivia_player (
    id uuid NOT NULL,
    personal_score integer NOT NULL,
    correct_answers integer NOT NULL,
    joined_at timestamp(0) without time zone NOT NULL,
    game_id uuid NOT NULL,
    team_id uuid,
    user_id uuid NOT NULL
);


--
-- Name: zchat_trivia_question; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.zchat_trivia_question (
    id uuid NOT NULL,
    category character varying(100) DEFAULT NULL::character varying,
    question text NOT NULL,
    answers json NOT NULL,
    difficulty integer NOT NULL,
    is_active boolean NOT NULL,
    times_asked integer NOT NULL,
    times_answered integer NOT NULL,
    created_at timestamp(0) without time zone NOT NULL
);


--
-- Name: zchat_trivia_team; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.zchat_trivia_team (
    id uuid NOT NULL,
    name character varying(50) NOT NULL,
    score integer NOT NULL,
    game_id uuid NOT NULL
);


--
-- Name: agent_action_log id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agent_action_log ALTER COLUMN id SET DEFAULT nextval('public.agent_action_log_id_seq'::regclass);


--
-- Name: asset_slot id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.asset_slot ALTER COLUMN id SET DEFAULT nextval('public.asset_slot_id_seq'::regclass);


--
-- Name: asset_state id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.asset_state ALTER COLUMN id SET DEFAULT nextval('public.asset_state_id_seq'::regclass);


--
-- Name: pay_ledger id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pay_ledger ALTER COLUMN id SET DEFAULT nextval('public.pay_ledger_id_seq'::regclass);


--
-- Name: structure_room id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.structure_room ALTER COLUMN id SET DEFAULT nextval('public.structure_subspace_id_seq'::regclass);


--
-- Name: actor_attribute actor_attribute_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.actor_attribute
    ADD CONSTRAINT actor_attribute_pkey PRIMARY KEY (actor_id, slug);


--
-- Name: actor actor_display_name_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.actor
    ADD CONSTRAINT actor_display_name_key UNIQUE (display_name);


--
-- Name: actor_dwell_credit actor_dwell_credit_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.actor_dwell_credit
    ADD CONSTRAINT actor_dwell_credit_pkey PRIMARY KEY (actor_id, object_id, attribute, source);


--
-- Name: actor_interaction_cooldown actor_interaction_cooldown_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.actor_interaction_cooldown
    ADD CONSTRAINT actor_interaction_cooldown_pkey PRIMARY KEY (speaker_id, listener_id, trigger);


--
-- Name: actor_inventory actor_inventory_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.actor_inventory
    ADD CONSTRAINT actor_inventory_pkey PRIMARY KEY (actor_id, item_kind);


--
-- Name: actor_known_place actor_known_place_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.actor_known_place
    ADD CONSTRAINT actor_known_place_pkey PRIMARY KEY (actor_id, place_ref);


--
-- Name: actor actor_login_username_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.actor
    ADD CONSTRAINT actor_login_username_key UNIQUE (login_username);


--
-- Name: actor_narrative_state actor_narrative_state_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.actor_narrative_state
    ADD CONSTRAINT actor_narrative_state_pkey PRIMARY KEY (actor_id);


--
-- Name: actor_need actor_need_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.actor_need
    ADD CONSTRAINT actor_need_pkey PRIMARY KEY (actor_id, key);


--
-- Name: actor actor_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.actor
    ADD CONSTRAINT actor_pkey PRIMARY KEY (id);


--
-- Name: actor_produce_state actor_produce_state_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.actor_produce_state
    ADD CONSTRAINT actor_produce_state_pkey PRIMARY KEY (actor_id, item_kind);


--
-- Name: actor_relationship actor_relationship_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.actor_relationship
    ADD CONSTRAINT actor_relationship_pkey PRIMARY KEY (actor_id, other_actor_id);


--
-- Name: agent_action_log agent_action_log_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agent_action_log
    ADD CONSTRAINT agent_action_log_pkey PRIMARY KEY (id);


--
-- Name: asset asset_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.asset
    ADD CONSTRAINT asset_pkey PRIMARY KEY (id);


--
-- Name: asset_slot asset_slot_asset_id_slot_name_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.asset_slot
    ADD CONSTRAINT asset_slot_asset_id_slot_name_key UNIQUE (asset_id, slot_name);


--
-- Name: asset_slot asset_slot_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.asset_slot
    ADD CONSTRAINT asset_slot_pkey PRIMARY KEY (id);


--
-- Name: asset_state asset_state_asset_id_state_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.asset_state
    ADD CONSTRAINT asset_state_asset_id_state_key UNIQUE (asset_id, state);


--
-- Name: asset_state_light asset_state_light_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.asset_state_light
    ADD CONSTRAINT asset_state_light_pkey PRIMARY KEY (state_id);


--
-- Name: asset_state asset_state_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.asset_state
    ADD CONSTRAINT asset_state_pkey PRIMARY KEY (id);


--
-- Name: asset_state_tag asset_state_tag_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.asset_state_tag
    ADD CONSTRAINT asset_state_tag_pkey PRIMARY KEY (state_id, tag);


--
-- Name: attribute_definition attribute_definition_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.attribute_definition
    ADD CONSTRAINT attribute_definition_pkey PRIMARY KEY (slug);


--
-- Name: huddle_member huddle_member_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.huddle_member
    ADD CONSTRAINT huddle_member_pkey PRIMARY KEY (huddle_id, actor_id);


--
-- Name: item_kind item_kind_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.item_kind
    ADD CONSTRAINT item_kind_pkey PRIMARY KEY (name);


--
-- Name: item_recipe item_recipe_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.item_recipe
    ADD CONSTRAINT item_recipe_pkey PRIMARY KEY (output_item);


--
-- Name: item_satisfies item_satisfies_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.item_satisfies
    ADD CONSTRAINT item_satisfies_pkey PRIMARY KEY (item_kind, attribute);


--
-- Name: messenger_messages messenger_messages_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.messenger_messages
    ADD CONSTRAINT messenger_messages_pkey PRIMARY KEY (id);


--
-- Name: migrations_applied migrations_applied_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.migrations_applied
    ADD CONSTRAINT migrations_applied_pkey PRIMARY KEY (migration_name);


--
-- Name: narration_pool_expansion narration_pool_expansion_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.narration_pool_expansion
    ADD CONSTRAINT narration_pool_expansion_pkey PRIMARY KEY (pool_key, phrase);


--
-- Name: npc_acquaintance npc_acquaintance_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.npc_acquaintance
    ADD CONSTRAINT npc_acquaintance_pkey PRIMARY KEY (actor_id, other_name);


--
-- Name: npc_sprite_animation npc_sprite_animation_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.npc_sprite_animation
    ADD CONSTRAINT npc_sprite_animation_pkey PRIMARY KEY (sprite_id, direction, animation);


--
-- Name: npc_sprite npc_sprite_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.npc_sprite
    ADD CONSTRAINT npc_sprite_pkey PRIMARY KEY (id);


--
-- Name: object_refresh object_refresh_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.object_refresh
    ADD CONSTRAINT object_refresh_pkey PRIMARY KEY (object_id, attribute);


--
-- Name: pay_ledger pay_ledger_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pay_ledger
    ADD CONSTRAINT pay_ledger_pkey PRIMARY KEY (id);


--
-- Name: refresh_attribute refresh_attribute_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.refresh_attribute
    ADD CONSTRAINT refresh_attribute_pkey PRIMARY KEY (name);


--
-- Name: scene_huddle scene_huddle_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.scene_huddle
    ADD CONSTRAINT scene_huddle_pkey PRIMARY KEY (id);


--
-- Name: scene_huddle_ref scene_huddle_ref_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.scene_huddle_ref
    ADD CONSTRAINT scene_huddle_ref_pkey PRIMARY KEY (scene_id, huddle_id);


--
-- Name: scene scene_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.scene
    ADD CONSTRAINT scene_pkey PRIMARY KEY (id);


--
-- Name: scene_quote scene_quote_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.scene_quote
    ADD CONSTRAINT scene_quote_pkey PRIMARY KEY (huddle_id, from_actor_id, item_kind);


--
-- Name: scenes scenes_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.scenes
    ADD CONSTRAINT scenes_pkey PRIMARY KEY (scene_id);


--
-- Name: setting setting_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.setting
    ADD CONSTRAINT setting_pkey PRIMARY KEY (key);


--
-- Name: structure structure_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.structure
    ADD CONSTRAINT structure_pkey PRIMARY KEY (id);


--
-- Name: structure_room structure_subspace_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.structure_room
    ADD CONSTRAINT structure_subspace_pkey PRIMARY KEY (id);


--
-- Name: structure_room structure_subspace_structure_id_name_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.structure_room
    ADD CONSTRAINT structure_subspace_structure_id_name_key UNIQUE (structure_id, name);


--
-- Name: room_access subspace_access_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.room_access
    ADD CONSTRAINT subspace_access_pkey PRIMARY KEY (room_id, actor_id);


--
-- Name: tileset_pack tileset_pack_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tileset_pack
    ADD CONSTRAINT tileset_pack_pkey PRIMARY KEY (id);


--
-- Name: user user_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."user"
    ADD CONSTRAINT user_pkey PRIMARY KEY (id);


--
-- Name: user_profile user_profile_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_profile
    ADD CONSTRAINT user_profile_pkey PRIMARY KEY (id);


--
-- Name: village_object village_object_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.village_object
    ADD CONSTRAINT village_object_pkey PRIMARY KEY (id);


--
-- Name: village_terrain village_terrain_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.village_terrain
    ADD CONSTRAINT village_terrain_pkey PRIMARY KEY (id);


--
-- Name: world_state world_phase_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.world_state
    ADD CONSTRAINT world_phase_pkey PRIMARY KEY (id);


--
-- Name: zchat_action zchat_action_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.zchat_action
    ADD CONSTRAINT zchat_action_pkey PRIMARY KEY (id);


--
-- Name: zchat_invite zchat_invite_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.zchat_invite
    ADD CONSTRAINT zchat_invite_pkey PRIMARY KEY (id);


--
-- Name: zchat_message zchat_message_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.zchat_message
    ADD CONSTRAINT zchat_message_pkey PRIMARY KEY (id);


--
-- Name: zchat_presence zchat_presence_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.zchat_presence
    ADD CONSTRAINT zchat_presence_pkey PRIMARY KEY (id);


--
-- Name: zchat_private_message zchat_private_message_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.zchat_private_message
    ADD CONSTRAINT zchat_private_message_pkey PRIMARY KEY (id);


--
-- Name: zchat_room zchat_room_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.zchat_room
    ADD CONSTRAINT zchat_room_pkey PRIMARY KEY (id);


--
-- Name: zchat_squelch zchat_squelch_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.zchat_squelch
    ADD CONSTRAINT zchat_squelch_pkey PRIMARY KEY (id);


--
-- Name: zchat_trivia_game zchat_trivia_game_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.zchat_trivia_game
    ADD CONSTRAINT zchat_trivia_game_pkey PRIMARY KEY (id);


--
-- Name: zchat_trivia_player zchat_trivia_player_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.zchat_trivia_player
    ADD CONSTRAINT zchat_trivia_player_pkey PRIMARY KEY (id);


--
-- Name: zchat_trivia_question zchat_trivia_question_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.zchat_trivia_question
    ADD CONSTRAINT zchat_trivia_question_pkey PRIMARY KEY (id);


--
-- Name: zchat_trivia_team zchat_trivia_team_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.zchat_trivia_team
    ADD CONSTRAINT zchat_trivia_team_pkey PRIMARY KEY (id);


--
-- Name: idx_30eba553efbcaebd; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_30eba553efbcaebd ON public.zchat_room USING btree (room_leader_id);


--
-- Name: idx_4928164e13bd9b9c; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_4928164e13bd9b9c ON public.zchat_squelch USING btree (squelched_user_id);


--
-- Name: idx_4928164ea76ed395; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_4928164ea76ed395 ON public.zchat_squelch USING btree (user_id);


--
-- Name: idx_5fc82364296cd8ae; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_5fc82364296cd8ae ON public.zchat_trivia_player USING btree (team_id);


--
-- Name: idx_5fc82364a76ed395; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_5fc82364a76ed395 ON public.zchat_trivia_player USING btree (user_id);


--
-- Name: idx_5fc82364e48fd905; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_5fc82364e48fd905 ON public.zchat_trivia_player USING btree (game_id);


--
-- Name: idx_642b55c954177093; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_642b55c954177093 ON public.zchat_trivia_game USING btree (room_id);


--
-- Name: idx_642b55c9a0f35d66; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_642b55c9a0f35d66 ON public.zchat_trivia_game USING btree (current_question_id);


--
-- Name: idx_75ea56e0fb7336f0e3bd61ce16ba31dbbf396750; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_75ea56e0fb7336f0e3bd61ce16ba31dbbf396750 ON public.messenger_messages USING btree (queue_name, available_at, delivered_at, id);


--
-- Name: idx_7e1139e054177093; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_7e1139e054177093 ON public.zchat_invite USING btree (room_id);


--
-- Name: idx_7e1139e0a7b4a7e3; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_7e1139e0a7b4a7e3 ON public.zchat_invite USING btree (invited_by_id);


--
-- Name: idx_7e1139e0c58dad6e; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_7e1139e0c58dad6e ON public.zchat_invite USING btree (invited_user_id);


--
-- Name: idx_837e4a04e92f8f78; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_837e4a04e92f8f78 ON public.zchat_private_message USING btree (recipient_id);


--
-- Name: idx_837e4a04f624b39d; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_837e4a04f624b39d ON public.zchat_private_message USING btree (sender_id);


--
-- Name: idx_83e0c25ae48fd905; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_83e0c25ae48fd905 ON public.zchat_trivia_team USING btree (game_id);


--
-- Name: idx_actor_attribute_slug; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_actor_attribute_slug ON public.actor_attribute USING btree (slug);


--
-- Name: idx_actor_attribute_snapshot_gen; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_actor_attribute_snapshot_gen ON public.actor_attribute USING btree (snapshot_gen);


--
-- Name: idx_actor_dwell_credit_snapshot_gen; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_actor_dwell_credit_snapshot_gen ON public.actor_dwell_credit USING btree (snapshot_gen);


--
-- Name: idx_actor_huddle; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_actor_huddle ON public.actor USING btree (current_huddle_id) WHERE (current_huddle_id IS NOT NULL);


--
-- Name: idx_actor_inside; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_actor_inside ON public.actor USING btree (inside_structure_id) WHERE (inside_structure_id IS NOT NULL);


--
-- Name: idx_actor_inventory_snapshot_gen; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_actor_inventory_snapshot_gen ON public.actor_inventory USING btree (snapshot_gen);


--
-- Name: idx_actor_known_place_snapshot_gen; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_actor_known_place_snapshot_gen ON public.actor_known_place USING btree (snapshot_gen);


--
-- Name: idx_actor_llm_memory_agent; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_actor_llm_memory_agent ON public.actor USING btree (llm_memory_agent) WHERE (llm_memory_agent IS NOT NULL);


--
-- Name: idx_actor_narrative_state_consolidation; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_actor_narrative_state_consolidation ON public.actor_narrative_state USING btree (last_consolidated_at NULLS FIRST);


--
-- Name: idx_actor_narrative_state_snapshot_gen; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_actor_narrative_state_snapshot_gen ON public.actor_narrative_state USING btree (snapshot_gen);


--
-- Name: idx_actor_need_snapshot_gen; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_actor_need_snapshot_gen ON public.actor_need USING btree (snapshot_gen);


--
-- Name: idx_actor_produce_state_snapshot_gen; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_actor_produce_state_snapshot_gen ON public.actor_produce_state USING btree (snapshot_gen);


--
-- Name: idx_actor_relationship_consolidation; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_actor_relationship_consolidation ON public.actor_relationship USING btree (last_consolidated_at NULLS FIRST) WHERE (jsonb_array_length(salient_facts) > 0);


--
-- Name: idx_actor_relationship_other; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_actor_relationship_other ON public.actor_relationship USING btree (other_actor_id);


--
-- Name: idx_actor_relationship_snapshot_gen; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_actor_relationship_snapshot_gen ON public.actor_relationship USING btree (snapshot_gen);


--
-- Name: idx_actor_snapshot_gen; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_actor_snapshot_gen ON public.actor USING btree (snapshot_gen);


--
-- Name: idx_agent_action_log_huddle; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_agent_action_log_huddle ON public.agent_action_log USING btree (huddle_id, occurred_at) WHERE (huddle_id IS NOT NULL);


--
-- Name: idx_agent_action_log_npc; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_agent_action_log_npc ON public.agent_action_log USING btree (actor_id, occurred_at DESC);


--
-- Name: idx_asset_fits_slot; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_asset_fits_slot ON public.asset USING btree (fits_slot) WHERE (fits_slot IS NOT NULL);


--
-- Name: idx_asset_slot_asset_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_asset_slot_asset_id ON public.asset_slot USING btree (asset_id);


--
-- Name: idx_asset_state_asset; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_asset_state_asset ON public.asset_state USING btree (asset_id);


--
-- Name: idx_asset_state_tag_tag; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_asset_state_tag_tag ON public.asset_state_tag USING btree (tag);


--
-- Name: idx_eb9665954177093; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_eb9665954177093 ON public.zchat_message USING btree (room_id);


--
-- Name: idx_eb966596c066afe; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_eb966596c066afe ON public.zchat_message USING btree (target_user_id);


--
-- Name: idx_eb96659f624b39d; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_eb96659f624b39d ON public.zchat_message USING btree (sender_id);


--
-- Name: idx_huddle_member_snapshot_gen; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_huddle_member_snapshot_gen ON public.huddle_member USING btree (snapshot_gen);


--
-- Name: idx_npc_acquaintance_other; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_npc_acquaintance_other ON public.npc_acquaintance USING btree (other_name);


--
-- Name: idx_npc_acquaintance_snapshot_gen; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_npc_acquaintance_snapshot_gen ON public.npc_acquaintance USING btree (snapshot_gen);


--
-- Name: idx_object_refresh_snapshot_gen; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_object_refresh_snapshot_gen ON public.object_refresh USING btree (snapshot_gen);


--
-- Name: idx_pay_ledger_pending_order_once; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_pay_ledger_pending_order_once ON public.pay_ledger USING btree (buyer_id, seller_id, item_kind) WHERE (((state)::text = 'accepted'::text) AND ((fulfillment_status)::text = 'pending'::text));


--
-- Name: idx_room_access_snapshot_gen; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_room_access_snapshot_gen ON public.room_access USING btree (snapshot_gen);


--
-- Name: idx_scene_huddle_active_structure; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_scene_huddle_active_structure ON public.scene_huddle USING btree (structure_id) WHERE (concluded_at IS NULL);


--
-- Name: idx_scene_huddle_ref_snapshot_gen; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_scene_huddle_ref_snapshot_gen ON public.scene_huddle_ref USING btree (snapshot_gen);


--
-- Name: idx_scene_huddle_snapshot_gen; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_scene_huddle_snapshot_gen ON public.scene_huddle USING btree (snapshot_gen);


--
-- Name: idx_scene_snapshot_gen; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_scene_snapshot_gen ON public.scene USING btree (snapshot_gen);


--
-- Name: idx_setting_public; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_setting_public ON public.setting USING btree (is_public);


--
-- Name: idx_structure_room_snapshot_gen; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_structure_room_snapshot_gen ON public.structure_room USING btree (snapshot_gen);


--
-- Name: idx_structure_snapshot_gen; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_structure_snapshot_gen ON public.structure USING btree (snapshot_gen);


--
-- Name: idx_village_object_asset; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_village_object_asset ON public.village_object USING btree (asset_id);


--
-- Name: idx_village_object_attached_to; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_village_object_attached_to ON public.village_object USING btree (attached_to) WHERE (attached_to IS NOT NULL);


--
-- Name: idx_village_object_owner; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_village_object_owner ON public.village_object USING btree (owner);


--
-- Name: idx_village_object_snapshot_gen; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_village_object_snapshot_gen ON public.village_object USING btree (snapshot_gen);


--
-- Name: idx_village_object_xy; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_village_object_xy ON public.village_object USING btree (x, y);


--
-- Name: idx_zchat_action_list; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_zchat_action_list ON public.zchat_action USING btree (action_list_slug, is_active);


--
-- Name: idx_zchat_invite_user; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_zchat_invite_user ON public.zchat_invite USING btree (invited_user_id, is_accepted);


--
-- Name: idx_zchat_message_room_order; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_zchat_message_room_order ON public.zchat_message USING btree (room_id, created_at);


--
-- Name: idx_zchat_message_sender_order; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_zchat_message_sender_order ON public.zchat_message USING btree (sender_id, created_at);


--
-- Name: idx_zchat_pm_conversation; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_zchat_pm_conversation ON public.zchat_private_message USING btree (sender_id, recipient_id, created_at);


--
-- Name: idx_zchat_pm_unread; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_zchat_pm_unread ON public.zchat_private_message USING btree (recipient_id, is_read);


--
-- Name: idx_zchat_presence_activity; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_zchat_presence_activity ON public.zchat_presence USING btree (last_activity_at);


--
-- Name: idx_zchat_presence_room; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_zchat_presence_room ON public.zchat_presence USING btree (room_id);


--
-- Name: idx_zchat_trivia_game_room; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_zchat_trivia_game_room ON public.zchat_trivia_game USING btree (room_id, status);


--
-- Name: idx_zchat_trivia_question_active; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_zchat_trivia_question_active ON public.zchat_trivia_question USING btree (is_active);


--
-- Name: idx_zchat_trivia_question_category; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_zchat_trivia_question_category ON public.zchat_trivia_question USING btree (category);


--
-- Name: ix_actor_dwell_credit_item_lcred; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_actor_dwell_credit_item_lcred ON public.actor_dwell_credit USING btree (last_credited_at) WHERE ((source)::text = 'item'::text);


--
-- Name: ix_actor_dwell_credit_object_lcred; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_actor_dwell_credit_object_lcred ON public.actor_dwell_credit USING btree (last_credited_at) WHERE ((source)::text = 'object'::text);


--
-- Name: ix_actor_inside_room; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_actor_inside_room ON public.actor USING btree (inside_room_id) WHERE (inside_room_id IS NOT NULL);


--
-- Name: ix_pay_ledger_buyer_seller; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_pay_ledger_buyer_seller ON public.pay_ledger USING btree (buyer_id, seller_id, item_kind, created_at DESC);


--
-- Name: ix_pay_ledger_outstanding; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_pay_ledger_outstanding ON public.pay_ledger USING btree (seller_id, ready_by, created_at) WHERE (((state)::text = 'accepted'::text) AND ((fulfillment_status)::text <> 'delivered'::text));


--
-- Name: ix_pay_ledger_pending; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_pay_ledger_pending ON public.pay_ledger USING btree (state, created_at) WHERE ((state)::text = 'pending'::text);


--
-- Name: ix_pay_ledger_scene_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_pay_ledger_scene_at ON public.pay_ledger USING btree (scene_id, created_at);


--
-- Name: ix_pay_ledger_v2_in_flight; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_pay_ledger_v2_in_flight ON public.pay_ledger USING btree (id) WHERE (((state)::text = 'accepted'::text) AND ((fulfillment_status)::text = ANY ((ARRAY['ready'::character varying, 'pending'::character varying])::text[])));


--
-- Name: ix_room_access_actor; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_room_access_actor ON public.room_access USING btree (actor_id);


--
-- Name: ix_scene_quote_huddle; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_scene_quote_huddle ON public.scene_quote USING btree (huddle_id);


--
-- Name: ix_scenes_structure; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_scenes_structure ON public.scenes USING btree (structure_id) WHERE (structure_id IS NOT NULL);


--
-- Name: ix_structure_room_structure; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_structure_room_structure ON public.structure_room USING btree (structure_id);


--
-- Name: pay_ledger_lodging_active_once; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX pay_ledger_lodging_active_once ON public.pay_ledger USING btree (buyer_id, seller_id, ready_by) WHERE (((item_kind)::text = 'nights_stay'::text) AND ((state)::text = 'accepted'::text) AND ((fulfillment_status)::text = 'delivered'::text));


--
-- Name: uniq_8d93d649e7927c74; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX uniq_8d93d649e7927c74 ON public."user" USING btree (email);


--
-- Name: uniq_8d93d649f85e0677; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX uniq_8d93d649f85e0677 ON public."user" USING btree (username);


--
-- Name: uniq_bbc2460ea76ed395; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX uniq_bbc2460ea76ed395 ON public.zchat_presence USING btree (user_id);


--
-- Name: uniq_d95ab405a76ed395; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX uniq_d95ab405a76ed395 ON public.user_profile USING btree (user_id);


--
-- Name: uniq_huddle_member_actor; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX uniq_huddle_member_actor ON public.huddle_member USING btree (actor_id);


--
-- Name: ux_room_access_one_private_active; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX ux_room_access_one_private_active ON public.room_access USING btree (room_id) WHERE ((kind = 'private'::public.room_kind) AND (active = true));


--
-- Name: zchat_trivia_unique_game_player; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX zchat_trivia_unique_game_player ON public.zchat_trivia_player USING btree (game_id, user_id);


--
-- Name: zchat_unique_action_slug; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX zchat_unique_action_slug ON public.zchat_action USING btree (slug);


--
-- Name: zchat_unique_room_slug; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX zchat_unique_room_slug ON public.zchat_room USING btree (slug);


--
-- Name: zchat_unique_user_squelch; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX zchat_unique_user_squelch ON public.zchat_squelch USING btree (user_id, squelched_user_id);


--
-- Name: actor_attribute actor_attribute_actor_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.actor_attribute
    ADD CONSTRAINT actor_attribute_actor_id_fkey FOREIGN KEY (actor_id) REFERENCES public.actor(id) ON DELETE CASCADE;


--
-- Name: actor_attribute actor_attribute_slug_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.actor_attribute
    ADD CONSTRAINT actor_attribute_slug_fkey FOREIGN KEY (slug) REFERENCES public.attribute_definition(slug) ON UPDATE CASCADE ON DELETE RESTRICT;


--
-- Name: actor_dwell_credit actor_dwell_credit_actor_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.actor_dwell_credit
    ADD CONSTRAINT actor_dwell_credit_actor_id_fkey FOREIGN KEY (actor_id) REFERENCES public.actor(id) ON DELETE CASCADE;


--
-- Name: actor_dwell_credit actor_dwell_credit_attribute_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.actor_dwell_credit
    ADD CONSTRAINT actor_dwell_credit_attribute_fkey FOREIGN KEY (attribute) REFERENCES public.refresh_attribute(name) ON UPDATE CASCADE;


--
-- Name: actor_dwell_credit actor_dwell_credit_object_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.actor_dwell_credit
    ADD CONSTRAINT actor_dwell_credit_object_id_fkey FOREIGN KEY (object_id) REFERENCES public.village_object(id) ON DELETE CASCADE;


--
-- Name: actor_interaction_cooldown actor_interaction_cooldown_listener_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.actor_interaction_cooldown
    ADD CONSTRAINT actor_interaction_cooldown_listener_id_fkey FOREIGN KEY (listener_id) REFERENCES public.actor(id) ON DELETE CASCADE;


--
-- Name: actor_interaction_cooldown actor_interaction_cooldown_speaker_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.actor_interaction_cooldown
    ADD CONSTRAINT actor_interaction_cooldown_speaker_id_fkey FOREIGN KEY (speaker_id) REFERENCES public.actor(id) ON DELETE CASCADE;


--
-- Name: actor_inventory actor_inventory_actor_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.actor_inventory
    ADD CONSTRAINT actor_inventory_actor_id_fkey FOREIGN KEY (actor_id) REFERENCES public.actor(id) ON DELETE CASCADE;


--
-- Name: actor_inventory actor_inventory_item_kind_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.actor_inventory
    ADD CONSTRAINT actor_inventory_item_kind_fkey FOREIGN KEY (item_kind) REFERENCES public.item_kind(name) ON UPDATE CASCADE;


--
-- Name: actor_known_place actor_known_place_actor_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.actor_known_place
    ADD CONSTRAINT actor_known_place_actor_id_fkey FOREIGN KEY (actor_id) REFERENCES public.actor(id) ON DELETE CASCADE;


--
-- Name: actor_narrative_state actor_narrative_state_actor_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.actor_narrative_state
    ADD CONSTRAINT actor_narrative_state_actor_id_fkey FOREIGN KEY (actor_id) REFERENCES public.actor(id) ON DELETE CASCADE;


--
-- Name: actor_need actor_need_actor_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.actor_need
    ADD CONSTRAINT actor_need_actor_id_fkey FOREIGN KEY (actor_id) REFERENCES public.actor(id) ON DELETE CASCADE;


--
-- Name: actor_produce_state actor_produce_state_actor_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.actor_produce_state
    ADD CONSTRAINT actor_produce_state_actor_id_fkey FOREIGN KEY (actor_id) REFERENCES public.actor(id) ON DELETE CASCADE;


--
-- Name: actor_produce_state actor_produce_state_item_kind_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.actor_produce_state
    ADD CONSTRAINT actor_produce_state_item_kind_fkey FOREIGN KEY (item_kind) REFERENCES public.item_kind(name) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: actor_relationship actor_relationship_actor_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.actor_relationship
    ADD CONSTRAINT actor_relationship_actor_id_fkey FOREIGN KEY (actor_id) REFERENCES public.actor(id) ON DELETE CASCADE;


--
-- Name: actor_relationship actor_relationship_other_actor_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.actor_relationship
    ADD CONSTRAINT actor_relationship_other_actor_id_fkey FOREIGN KEY (other_actor_id) REFERENCES public.actor(id) ON DELETE CASCADE;


--
-- Name: actor actor_sprite_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.actor
    ADD CONSTRAINT actor_sprite_id_fkey FOREIGN KEY (sprite_id) REFERENCES public.npc_sprite(id);


--
-- Name: agent_action_log agent_action_log_actor_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agent_action_log
    ADD CONSTRAINT agent_action_log_actor_id_fkey FOREIGN KEY (actor_id) REFERENCES public.actor(id) ON DELETE CASCADE;


--
-- Name: asset_state_light asset_state_light_state_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.asset_state_light
    ADD CONSTRAINT asset_state_light_state_id_fkey FOREIGN KEY (state_id) REFERENCES public.asset_state(id) ON DELETE CASCADE;


--
-- Name: asset_state_tag asset_state_tag_state_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.asset_state_tag
    ADD CONSTRAINT asset_state_tag_state_id_fkey FOREIGN KEY (state_id) REFERENCES public.asset_state(id) ON DELETE CASCADE;


--
-- Name: zchat_room fk_30eba553efbcaebd; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.zchat_room
    ADD CONSTRAINT fk_30eba553efbcaebd FOREIGN KEY (room_leader_id) REFERENCES public."user"(id);


--
-- Name: zchat_squelch fk_4928164e13bd9b9c; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.zchat_squelch
    ADD CONSTRAINT fk_4928164e13bd9b9c FOREIGN KEY (squelched_user_id) REFERENCES public."user"(id);


--
-- Name: zchat_squelch fk_4928164ea76ed395; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.zchat_squelch
    ADD CONSTRAINT fk_4928164ea76ed395 FOREIGN KEY (user_id) REFERENCES public."user"(id);


--
-- Name: zchat_trivia_player fk_5fc82364296cd8ae; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.zchat_trivia_player
    ADD CONSTRAINT fk_5fc82364296cd8ae FOREIGN KEY (team_id) REFERENCES public.zchat_trivia_team(id);


--
-- Name: zchat_trivia_player fk_5fc82364a76ed395; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.zchat_trivia_player
    ADD CONSTRAINT fk_5fc82364a76ed395 FOREIGN KEY (user_id) REFERENCES public."user"(id);


--
-- Name: zchat_trivia_player fk_5fc82364e48fd905; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.zchat_trivia_player
    ADD CONSTRAINT fk_5fc82364e48fd905 FOREIGN KEY (game_id) REFERENCES public.zchat_trivia_game(id);


--
-- Name: zchat_trivia_game fk_642b55c954177093; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.zchat_trivia_game
    ADD CONSTRAINT fk_642b55c954177093 FOREIGN KEY (room_id) REFERENCES public.zchat_room(id);


--
-- Name: zchat_trivia_game fk_642b55c9a0f35d66; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.zchat_trivia_game
    ADD CONSTRAINT fk_642b55c9a0f35d66 FOREIGN KEY (current_question_id) REFERENCES public.zchat_trivia_question(id);


--
-- Name: zchat_invite fk_7e1139e054177093; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.zchat_invite
    ADD CONSTRAINT fk_7e1139e054177093 FOREIGN KEY (room_id) REFERENCES public.zchat_room(id);


--
-- Name: zchat_invite fk_7e1139e0a7b4a7e3; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.zchat_invite
    ADD CONSTRAINT fk_7e1139e0a7b4a7e3 FOREIGN KEY (invited_by_id) REFERENCES public."user"(id);


--
-- Name: zchat_invite fk_7e1139e0c58dad6e; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.zchat_invite
    ADD CONSTRAINT fk_7e1139e0c58dad6e FOREIGN KEY (invited_user_id) REFERENCES public."user"(id);


--
-- Name: zchat_private_message fk_837e4a04e92f8f78; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.zchat_private_message
    ADD CONSTRAINT fk_837e4a04e92f8f78 FOREIGN KEY (recipient_id) REFERENCES public."user"(id);


--
-- Name: zchat_private_message fk_837e4a04f624b39d; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.zchat_private_message
    ADD CONSTRAINT fk_837e4a04f624b39d FOREIGN KEY (sender_id) REFERENCES public."user"(id);


--
-- Name: zchat_trivia_team fk_83e0c25ae48fd905; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.zchat_trivia_team
    ADD CONSTRAINT fk_83e0c25ae48fd905 FOREIGN KEY (game_id) REFERENCES public.zchat_trivia_game(id);


--
-- Name: zchat_presence fk_bbc2460e54177093; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.zchat_presence
    ADD CONSTRAINT fk_bbc2460e54177093 FOREIGN KEY (room_id) REFERENCES public.zchat_room(id);


--
-- Name: zchat_presence fk_bbc2460ea76ed395; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.zchat_presence
    ADD CONSTRAINT fk_bbc2460ea76ed395 FOREIGN KEY (user_id) REFERENCES public."user"(id);


--
-- Name: user_profile fk_d95ab405a76ed395; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_profile
    ADD CONSTRAINT fk_d95ab405a76ed395 FOREIGN KEY (user_id) REFERENCES public."user"(id);


--
-- Name: zchat_message fk_eb9665954177093; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.zchat_message
    ADD CONSTRAINT fk_eb9665954177093 FOREIGN KEY (room_id) REFERENCES public.zchat_room(id);


--
-- Name: zchat_message fk_eb966596c066afe; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.zchat_message
    ADD CONSTRAINT fk_eb966596c066afe FOREIGN KEY (target_user_id) REFERENCES public."user"(id);


--
-- Name: zchat_message fk_eb96659f624b39d; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.zchat_message
    ADD CONSTRAINT fk_eb96659f624b39d FOREIGN KEY (sender_id) REFERENCES public."user"(id);


--
-- Name: huddle_member huddle_member_huddle_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.huddle_member
    ADD CONSTRAINT huddle_member_huddle_id_fkey FOREIGN KEY (huddle_id) REFERENCES public.scene_huddle(id) ON DELETE CASCADE;


--
-- Name: item_recipe item_recipe_output_item_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.item_recipe
    ADD CONSTRAINT item_recipe_output_item_fkey FOREIGN KEY (output_item) REFERENCES public.item_kind(name) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: item_satisfies item_satisfies_item_kind_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.item_satisfies
    ADD CONSTRAINT item_satisfies_item_kind_fkey FOREIGN KEY (item_kind) REFERENCES public.item_kind(name) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: npc_acquaintance npc_acquaintance_actor_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.npc_acquaintance
    ADD CONSTRAINT npc_acquaintance_actor_id_fkey FOREIGN KEY (actor_id) REFERENCES public.actor(id) ON DELETE CASCADE;


--
-- Name: npc_sprite_animation npc_sprite_animation_sprite_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.npc_sprite_animation
    ADD CONSTRAINT npc_sprite_animation_sprite_id_fkey FOREIGN KEY (sprite_id) REFERENCES public.npc_sprite(id) ON DELETE CASCADE;


--
-- Name: npc_sprite npc_sprite_pack_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.npc_sprite
    ADD CONSTRAINT npc_sprite_pack_id_fkey FOREIGN KEY (pack_id) REFERENCES public.tileset_pack(id);


--
-- Name: object_refresh object_refresh_attribute_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.object_refresh
    ADD CONSTRAINT object_refresh_attribute_fk FOREIGN KEY (attribute) REFERENCES public.refresh_attribute(name) ON UPDATE CASCADE;


--
-- Name: object_refresh object_refresh_object_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.object_refresh
    ADD CONSTRAINT object_refresh_object_id_fkey FOREIGN KEY (object_id) REFERENCES public.village_object(id) ON DELETE CASCADE;


--
-- Name: pay_ledger pay_ledger_item_kind_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pay_ledger
    ADD CONSTRAINT pay_ledger_item_kind_fkey FOREIGN KEY (item_kind) REFERENCES public.item_kind(name) ON UPDATE CASCADE;


--
-- Name: pay_ledger pay_ledger_parent_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pay_ledger
    ADD CONSTRAINT pay_ledger_parent_id_fkey FOREIGN KEY (parent_id) REFERENCES public.pay_ledger(id);


--
-- Name: scene_huddle_ref scene_huddle_ref_scene_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.scene_huddle_ref
    ADD CONSTRAINT scene_huddle_ref_scene_id_fkey FOREIGN KEY (scene_id) REFERENCES public.scene(id) ON DELETE CASCADE;


--
-- Name: scene_quote scene_quote_from_actor_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.scene_quote
    ADD CONSTRAINT scene_quote_from_actor_id_fkey FOREIGN KEY (from_actor_id) REFERENCES public.actor(id) ON DELETE CASCADE;


--
-- Name: scene_quote scene_quote_item_kind_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.scene_quote
    ADD CONSTRAINT scene_quote_item_kind_fkey FOREIGN KEY (item_kind) REFERENCES public.item_kind(name) ON UPDATE CASCADE;


--
-- Name: scenes scenes_structure_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.scenes
    ADD CONSTRAINT scenes_structure_id_fkey FOREIGN KEY (structure_id) REFERENCES public.village_object(id) ON DELETE SET NULL;


--
-- Name: structure_room structure_room_structure_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.structure_room
    ADD CONSTRAINT structure_room_structure_id_fkey FOREIGN KEY (structure_id) REFERENCES public.structure(id) ON DELETE CASCADE;


--
-- Name: room_access subspace_access_actor_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.room_access
    ADD CONSTRAINT subspace_access_actor_id_fkey FOREIGN KEY (actor_id) REFERENCES public.actor(id) ON DELETE CASCADE;


--
-- Name: room_access subspace_access_granted_via_ledger_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.room_access
    ADD CONSTRAINT subspace_access_granted_via_ledger_id_fkey FOREIGN KEY (granted_via_ledger_id) REFERENCES public.pay_ledger(id) ON DELETE SET NULL;


--
-- Name: room_access subspace_access_subspace_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.room_access
    ADD CONSTRAINT subspace_access_subspace_id_fkey FOREIGN KEY (room_id) REFERENCES public.structure_room(id) ON DELETE CASCADE;


--
-- Name: village_object village_object_attached_to_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.village_object
    ADD CONSTRAINT village_object_attached_to_fkey FOREIGN KEY (attached_to) REFERENCES public.village_object(id) ON DELETE CASCADE DEFERRABLE INITIALLY DEFERRED;


--
-- PostgreSQL database dump complete
--

\unrestrict b8FeRKxG7OGNF1plKr1MguXZbgfTr1amgP9YtJij9e6eUJyGemQbPVKUsMFaKF1

