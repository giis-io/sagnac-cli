# Politique de sécurité

GIIS, 66 avenue des Champs-Élysées, 75008 Paris, est le fabricant de `sagnac`.

## Périmètre

- la ligne de commande `sagnac` de ce dépôt ;
- les kits de labs qu'elle télécharge et exécute sur le poste ;
- les routes de la plateforme que la ligne de commande appelle : connexion de
  l'appareil et jetons, téléchargement des kits, sessions, envoi des tentatives,
  récupération des énoncés, fichier de version.

Le reste du site (pages publiques, paiement, compte web) n'est pas dans ce
périmètre. Un signalement qui le concerne est tout de même lu et transmis.

## Signaler une vulnérabilité

- par e-mail à securite@sagnac.fr ;
- ou par le signalement privé de GitHub sur ce dépôt (onglet Security,
  « Report a vulnerability »).

N'ouvrez pas de ticket public. Indiquez la version (`sagnac version`), votre
système, les étapes pour reproduire et l'effet constaté.

## Ce que vous pouvez attendre

- un accusé de réception sous 3 jours ouvrés (objectif) ;
- une divulgation coordonnée, 90 jours au plus après le signalement, plus tôt si
  un correctif est publié avant ;
- votre nom dans l'avis publié, si vous le souhaitez.

GIIS ne verse pas de prime. GIIS n'engagera pas d'action en justice contre une
recherche menée de bonne foi dans le périmètre ci-dessus : pas d'accès aux
données d'autres personnes, pas de déni de service, pas de conservation des
données rencontrées.

## Versions corrigées

Seule la dernière version reçoit les correctifs. La mise à jour est gratuite et
publique, sans compte. Le support est assuré jusqu'en décembre 2032 ; la même
date s'affiche avec `sagnac version`.

## Avis

Les vulnérabilités corrigées sont publiées dans les avis de sécurité (GitHub
Security Advisories) de ce dépôt, avec les versions touchées, l'effet, la gravité
et le remède.
