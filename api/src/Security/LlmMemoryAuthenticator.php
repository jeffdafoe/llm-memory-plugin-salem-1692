<?php

namespace App\Security;

use Symfony\Component\HttpFoundation\JsonResponse;
use Symfony\Component\HttpFoundation\Request;
use Symfony\Component\HttpFoundation\Response;
use Symfony\Component\Security\Core\Authentication\Token\TokenInterface;
use Symfony\Component\Security\Core\Exception\AuthenticationException;
use Symfony\Component\Security\Core\Exception\CustomUserMessageAuthenticationException;
use Symfony\Component\Security\Http\Authenticator\AbstractAuthenticator;
use Symfony\Component\Security\Http\Authenticator\Passport\Badge\UserBadge;
use Symfony\Component\Security\Http\Authenticator\Passport\Passport;
use Symfony\Component\Security\Http\Authenticator\Passport\SelfValidatingPassport;
use Symfony\Contracts\HttpClient\HttpClientInterface;

class LlmMemoryAuthenticator extends AbstractAuthenticator
{
    private string $verifyUrl;

    public function __construct(
        private HttpClientInterface $httpClient,
        string $llmMemoryApiUrl = 'http://127.0.0.1:3100'
    ) {
        $this->verifyUrl = $llmMemoryApiUrl . '/v1/auth/verify';
    }

    public function supports(Request $request): ?bool
    {
        return $request->headers->has('Authorization');
    }

    public function authenticate(Request $request): Passport
    {
        $authHeader = $request->headers->get('Authorization', '');
        $token = str_replace('Bearer ', '', $authHeader);

        if (empty($token)) {
            throw new CustomUserMessageAuthenticationException('Missing session token');
        }

        // Call llm-memory to verify the token
        try {
            $response = $this->httpClient->request('POST', $this->verifyUrl, [
                'json' => ['token' => $token],
                'timeout' => 5,
            ]);

            $data = $response->toArray(false);
        } catch (\Exception $e) {
            throw new CustomUserMessageAuthenticationException('Auth service unavailable');
        }

        if (empty($data['valid'])) {
            throw new CustomUserMessageAuthenticationException('Invalid or expired session token');
        }

        $agentName = $data['agent'] ?? 'unknown';

        // Create a self-validating passport — llm-memory already verified the identity.
        // The UserBadge identifier is the llm-memory agent name.
        return new SelfValidatingPassport(
            new UserBadge($agentName, function (string $identifier) use ($data) {
                return new LlmMemoryUser(
                    $identifier,
                    $data['actor_id'] ?? null
                );
            })
        );
    }

    public function onAuthenticationSuccess(Request $request, TokenInterface $token, string $firewallName): ?Response
    {
        // Let the request continue to the controller
        return null;
    }

    public function onAuthenticationFailure(Request $request, AuthenticationException $exception): ?Response
    {
        return new JsonResponse(
            ['error' => $exception->getMessageKey()],
            Response::HTTP_UNAUTHORIZED
        );
    }
}
