<?php

namespace App\Controller;

use Doctrine\DBAL\Connection;
use Symfony\Bundle\FrameworkBundle\Controller\AbstractController;
use Symfony\Component\HttpFoundation\JsonResponse;
use Symfony\Component\Routing\Attribute\Route;
use Symfony\Component\Security\Http\Attribute\IsGranted;

#[IsGranted('ROLE_VILLAGE_USER')]
class VillageController extends AbstractController
{
    public function __construct(
        private Connection $connection
    ) {}

    #[Route('/api/village/buildings', name: 'api_village_buildings', methods: ['GET'])]
    public function buildings(): JsonResponse
    {
        $buildings = $this->connection->fetchAllAssociative('
            SELECT
                vb.id,
                vb.tile_x,
                vb.tile_y,
                vb.building_style,
                vb.building_variant
            FROM village_building vb
            ORDER BY vb.created_at
        ');

        $residents = $this->connection->fetchAllAssociative('
            SELECT
                vbr.building_id,
                va.name,
                va.llm_memory_agent,
                va.role,
                va.coins,
                va.is_virtual
            FROM village_building_resident vbr
            JOIN village_agent va ON va.id = vbr.agent_id
            ORDER BY vbr.moved_in_at
        ');

        // Group residents by building
        $residentsByBuilding = [];
        foreach ($residents as $resident) {
            $buildingId = $resident['building_id'];
            unset($resident['building_id']);
            $resident['is_virtual'] = (bool) $resident['is_virtual'];
            $residentsByBuilding[$buildingId][] = $resident;
        }

        // Attach residents to buildings
        $result = [];
        foreach ($buildings as $building) {
            $building['tile_x'] = (int) $building['tile_x'];
            $building['tile_y'] = (int) $building['tile_y'];
            $building['building_variant'] = (int) $building['building_variant'];
            $building['residents'] = $residentsByBuilding[$building['id']] ?? [];
            $result[] = $building;
        }

        return $this->json($result);
    }
}
