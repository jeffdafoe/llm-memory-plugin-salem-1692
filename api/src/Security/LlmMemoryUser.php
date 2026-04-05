<?php

namespace App\Security;

use Symfony\Component\Security\Core\User\UserInterface;

class LlmMemoryUser implements UserInterface
{
    private string $agentName;
    private ?string $actorId;

    public function __construct(string $agentName, ?string $actorId = null)
    {
        $this->agentName = $agentName;
        $this->actorId = $actorId;
    }

    public function getAgentName(): string
    {
        return $this->agentName;
    }

    public function getActorId(): ?string
    {
        return $this->actorId;
    }

    public function getUserIdentifier(): string
    {
        return $this->agentName;
    }

    public function getRoles(): array
    {
        return ['ROLE_VILLAGE_USER'];
    }

    public function eraseCredentials(): void
    {
        // Nothing to erase — token-based auth
    }
}
