-- LLM-365 down: recreate the dead v1 `summon_errand` table (reverse of the
-- drop). Restores the table, its primary key, four indexes, and five foreign
-- keys exactly as they stood in the pre-365 schema baseline. The table is inert
-- — the v2 engine never touches it — so this exists purely for reversibility.

BEGIN;

CREATE TABLE public.summon_errand (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    summoner_id uuid NOT NULL,
    messenger_id uuid NOT NULL,
    target_name text NOT NULL,
    summon_point_id uuid NOT NULL,
    reason text DEFAULT ''::text NOT NULL,
    state text NOT NULL,
    target_kind text NOT NULL,
    messenger_origin_x double precision NOT NULL,
    messenger_origin_y double precision NOT NULL,
    target_dispatch_x double precision NOT NULL,
    target_dispatch_y double precision NOT NULL,
    chat_at_summon_until timestamp with time zone,
    chat_at_target_until timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    messenger_origin_structure_id uuid,
    target_dispatch_structure_id uuid,
    CONSTRAINT summon_errand_state_check CHECK ((state = ANY (ARRAY['dispatched'::text, 'summoner_at_point'::text, 'messenger_at_point'::text, 'messenger_to_target'::text, 'messenger_at_target'::text, 'messenger_to_summoner'::text, 'messenger_returning'::text, 'done'::text, 'failed'::text]))),
    CONSTRAINT summon_errand_target_kind_check CHECK ((target_kind = ANY (ARRAY['va'::text, 'pc'::text, 'nonva'::text, 'unknown'::text])))
);

ALTER TABLE ONLY public.summon_errand
    ADD CONSTRAINT summon_errand_pkey PRIMARY KEY (id);

CREATE INDEX idx_summon_errand_active ON public.summon_errand USING btree (state) WHERE (state <> ALL (ARRAY['done'::text, 'failed'::text]));
CREATE UNIQUE INDEX idx_summon_errand_messenger_active ON public.summon_errand USING btree (messenger_id) WHERE (state <> ALL (ARRAY['done'::text, 'failed'::text]));
CREATE INDEX idx_summon_errand_messenger_id ON public.summon_errand USING btree (messenger_id);
CREATE INDEX idx_summon_errand_summoner_active ON public.summon_errand USING btree (summoner_id) WHERE (state <> ALL (ARRAY['done'::text, 'failed'::text]));

ALTER TABLE ONLY public.summon_errand
    ADD CONSTRAINT summon_errand_messenger_id_fkey FOREIGN KEY (messenger_id) REFERENCES public.actor(id) ON DELETE CASCADE;
ALTER TABLE ONLY public.summon_errand
    ADD CONSTRAINT summon_errand_messenger_origin_structure_id_fkey FOREIGN KEY (messenger_origin_structure_id) REFERENCES public.village_object(id) ON DELETE SET NULL;
ALTER TABLE ONLY public.summon_errand
    ADD CONSTRAINT summon_errand_summon_point_id_fkey FOREIGN KEY (summon_point_id) REFERENCES public.village_object(id) ON DELETE CASCADE;
ALTER TABLE ONLY public.summon_errand
    ADD CONSTRAINT summon_errand_summoner_id_fkey FOREIGN KEY (summoner_id) REFERENCES public.actor(id) ON DELETE CASCADE;
ALTER TABLE ONLY public.summon_errand
    ADD CONSTRAINT summon_errand_target_dispatch_structure_id_fkey FOREIGN KEY (target_dispatch_structure_id) REFERENCES public.village_object(id) ON DELETE SET NULL;

COMMIT;
