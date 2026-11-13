# Changelog

## [0.2.0](https://github.com/Serraniel/nepfleet/compare/v0.1.0...v0.2.0) (2026-11-13)


### Features

* **api:** edit stack files over the API, with an edit scope ([#239](https://github.com/Serraniel/nepfleet/issues/239)) ([083b9c1](https://github.com/Serraniel/nepfleet/commit/083b9c1865b38f374cdfc3b83cda0b53c5ef26ed))
* **api:** stack state in the stack list ([#241](https://github.com/Serraniel/nepfleet/issues/241)) ([df080f4](https://github.com/Serraniel/nepfleet/commit/df080f46403fb86e1b5355cd872c29f367e9600b))
* Arch package for the CLI (nepfleet-bin) ([#234](https://github.com/Serraniel/nepfleet/issues/234)) ([da5684c](https://github.com/Serraniel/nepfleet/commit/da5684c5e441b05210cfbd5862970784be833a46))
* **control:** prepare and install a remote from the local daemon ([#238](https://github.com/Serraniel/nepfleet/issues/238)) ([dddc409](https://github.com/Serraniel/nepfleet/commit/dddc40929b827b97d2786e6f1a652b773b0c25fc))
* **doctor:** explain port 53 held by systemd-resolved ([#248](https://github.com/Serraniel/nepfleet/issues/248)) ([9ab26f7](https://github.com/Serraniel/nepfleet/commit/9ab26f742c0107d78fd06fc954118cbe69ca94ac))
* nepfleet host install sets NepFleet up on a machine over SSH ([#233](https://github.com/Serraniel/nepfleet/issues/233)) ([7b5f0bd](https://github.com/Serraniel/nepfleet/commit/7b5f0bd07d98e4304826934065a5efce4d5cda7b))
* template files the app edits are written only when missing ([#242](https://github.com/Serraniel/nepfleet/issues/242)) ([5568fdb](https://github.com/Serraniel/nepfleet/commit/5568fdb953e281e35a097784ae3d4194a2103550))


### Bug Fixes

* register binfmt emulators again when serve starts after a reboot ([#240](https://github.com/Serraniel/nepfleet/issues/240)) ([c0e89cc](https://github.com/Serraniel/nepfleet/commit/c0e89cc690ce9fa2ea361438bd06e0b8fc44d8ad))
* say that a key with a passphrase must be in the SSH agent ([#231](https://github.com/Serraniel/nepfleet/issues/231)) ([a23aab0](https://github.com/Serraniel/nepfleet/commit/a23aab048be8ee8750b7c805d384c2077a526fa9))


### Dependencies

* Bump alpine from `be26560` to `f59abe6` ([#246](https://github.com/Serraniel/nepfleet/issues/246)) ([9fd5253](https://github.com/Serraniel/nepfleet/commit/9fd52530df84876aae13dfb86f826f5261f921fe))
* Bump golang from `d200d2d` to `fa3d596` ([#237](https://github.com/Serraniel/nepfleet/issues/237)) ([96a5628](https://github.com/Serraniel/nepfleet/commit/96a5628d02a1972b9d71c293aa0aa03102449c7a))
* Bump the go group with 3 updates ([#243](https://github.com/Serraniel/nepfleet/issues/243)) ([a7a59dd](https://github.com/Serraniel/nepfleet/commit/a7a59dddf8152dcdeea7df377fb5f4b08410e6a4))

## 0.1.0 (2026-11-08)


### Security

* **deps:** Bump github.com/go-jose/go-jose/v4 from 4.1.4 to 4.1.6 (GHSA-4w3g-jfj2-cw2q, GHSA-mgcc-cww5-fp2q, GHSA-vvm8-gf78-28g4) ([#191](https://github.com/Serraniel/nepfleet/issues/191)) ([464c4f5](https://github.com/Serraniel/nepfleet/commit/464c4f50123d9d61da7d15c4bc2a5208915677be))


### Features

* a template can name the service its bot check goes in front of ([#218](https://github.com/Serraniel/nepfleet/issues/218)) ([85cce90](https://github.com/Serraniel/nepfleet/commit/85cce90c4e5835f63c2366735b3047ea14dbf11a))
* access modes: stacks on a LAN port instead of a domain ([#145](https://github.com/Serraniel/nepfleet/issues/145)) ([2acd61a](https://github.com/Serraniel/nepfleet/commit/2acd61acb252a85ed1c1c5164675ab6b59311e77))
* adding a passkey needs a recent proof it's still you ([#214](https://github.com/Serraniel/nepfleet/issues/214)) ([5e00382](https://github.com/Serraniel/nepfleet/commit/5e00382538cacf4b5ed8b8b3d0142892b7c70fcd))
* alerts from other tools through the notification channels, with a notify token scope ([#212](https://github.com/Serraniel/nepfleet/issues/212)) ([3612b42](https://github.com/Serraniel/nepfleet/commit/3612b42335608162816c6929541c3151d378ddff))
* API applies the reviewed plan as a background job ([#149](https://github.com/Serraniel/nepfleet/issues/149)) ([164586f](https://github.com/Serraniel/nepfleet/commit/164586f67ae71f9956f0fe09efe0f89a1804f981))
* API lists the fleet's template catalogues ([#166](https://github.com/Serraniel/nepfleet/issues/166)) ([cde4343](https://github.com/Serraniel/nepfleet/commit/cde4343829fc2f21a34b2cc23f970cd229fe877f))
* API reads a stack's plan and history ([#148](https://github.com/Serraniel/nepfleet/issues/148)) ([d5593a8](https://github.com/Serraniel/nepfleet/commit/d5593a8980acc8639ec7bc6db871dfef30eba6ec))
* API serves TLS and refuses plain HTTP beyond loopback ([#150](https://github.com/Serraniel/nepfleet/issues/150)) ([5992364](https://github.com/Serraniel/nepfleet/commit/599236498a2200b1788c2b37c045abbeafaabe13))
* apply refuses images this host cannot run, unless emulated ([#100](https://github.com/Serraniel/nepfleet/issues/100)) ([941275b](https://github.com/Serraniel/nepfleet/commit/941275b80a5b3204f605d5e0d2b917675f454020))
* automatic image updates, for a timer ([#195](https://github.com/Serraniel/nepfleet/issues/195)) ([202857f](https://github.com/Serraniel/nepfleet/commit/202857f46906187bc0e755d76fb8e0c5c93ff217))
* block known telemetry hosts in every service ([#136](https://github.com/Serraniel/nepfleet/issues/136)) ([2d45b58](https://github.com/Serraniel/nepfleet/commit/2d45b58b7974e8797fef9e9b7ecb89eaa25c7b98))
* builtins for backups: the fleet's age recipients and volumes not to back up ([#132](https://github.com/Serraniel/nepfleet/issues/132)) ([6f2d4ad](https://github.com/Serraniel/nepfleet/commit/6f2d4ad1de48762aa3157b71a0003d43ab98719b))
* check a host's security, over SSH or locally ([#178](https://github.com/Serraniel/nepfleet/issues/178)) ([4bc8d3b](https://github.com/Serraniel/nepfleet/commit/4bc8d3b8f42c52352564b258719660c043b6ed9c))
* **cli:** nepfleet new, a wizard that creates a stack from a template ([#94](https://github.com/Serraniel/nepfleet/issues/94)) ([b06ab7e](https://github.com/Serraniel/nepfleet/commit/b06ab7eb0d9b8b8abb0e8b6dcdeaf0287ca711a2))
* config files versioned with a stack in stacks/&lt;name&gt;/files/ ([#151](https://github.com/Serraniel/nepfleet/issues/151)) ([f0a5c80](https://github.com/Serraniel/nepfleet/commit/f0a5c8018ca81b7dcd3f22b3a078855275626c66))
* connect to a remote fleet through an SSH tunnel ([#168](https://github.com/Serraniel/nepfleet/issues/168)) ([54cdc34](https://github.com/Serraniel/nepfleet/commit/54cdc34c5bf72e009df7322edf6838f535969fe0))
* control daemon on a guarded loopback port for browsers ([#169](https://github.com/Serraniel/nepfleet/issues/169)) ([459e00f](https://github.com/Serraniel/nepfleet/commit/459e00f40b47cc699f7fa06ba96b9b3f9b837ced))
* copies of the age key and mirrors of the fleet repository ([#123](https://github.com/Serraniel/nepfleet/issues/123)) ([584f01b](https://github.com/Serraniel/nepfleet/commit/584f01bfc8a6a2f61629ee176e6d02e290a7a937))
* cross-stack values: use another stack's value or secret ([#121](https://github.com/Serraniel/nepfleet/issues/121)) ([e1cac00](https://github.com/Serraniel/nepfleet/commit/e1cac0065debafde818abb279429012eaccf8e5d))
* data-safe rollback in nepfleet apply ([#117](https://github.com/Serraniel/nepfleet/issues/117)) ([fef131f](https://github.com/Serraniel/nepfleet/commit/fef131f3c903ef02b91957524441817f5a371202))
* deny a stack's outbound traffic unless it is allowed ([#184](https://github.com/Serraniel/nepfleet/issues/184)) ([f26f262](https://github.com/Serraniel/nepfleet/commit/f26f262629361b593487246be86ce0c612e3af41))
* describe the API in OpenAPI, and serve the document ([#187](https://github.com/Serraniel/nepfleet/issues/187)) ([2ada95c](https://github.com/Serraniel/nepfleet/commit/2ada95c0ecaecc62357d13c6cf801f225cfe5052))
* device and gpu inputs: USB sticks, /dev/dri and NVIDIA in templates ([#118](https://github.com/Serraniel/nepfleet/issues/118)) ([4232139](https://github.com/Serraniel/nepfleet/commit/4232139fd511a51574a390a861e3eb170796c2e8))
* doctor lists stacks with egress limited or recorded ([#217](https://github.com/Serraniel/nepfleet/issues/217)) ([c2f76d1](https://github.com/Serraniel/nepfleet/commit/c2f76d19bdda17a334c35e9e17af4bf1d58aaa3e))
* **doctor:** check that a host can run what is planned for it ([#90](https://github.com/Serraniel/nepfleet/issues/90)) ([ebb2061](https://github.com/Serraniel/nepfleet/commit/ebb2061b76f72d9e67324164eba635c9481bcd95))
* draft a template from a compose file ([#194](https://github.com/Serraniel/nepfleet/issues/194)) ([c98d8aa](https://github.com/Serraniel/nepfleet/commit/c98d8aa9acac21b7d299ea212578b37b41609ea4))
* egress enforce logs what it refuses, and suggest names it ([#215](https://github.com/Serraniel/nepfleet/issues/215)) ([a93322b](https://github.com/Serraniel/nepfleet/commit/a93322bd1d50b0454c91fcb890d1d30e4bbffadb))
* egress mode log, and an allowlist from what a stack did ([#202](https://github.com/Serraniel/nepfleet/issues/202)) ([60bb539](https://github.com/Serraniel/nepfleet/commit/60bb539d9efcebe553673d142655f83b3e6cee2d))
* first run creates a status page; restore takes the stack lock ([#171](https://github.com/Serraniel/nepfleet/issues/171)) ([cfc14c9](https://github.com/Serraniel/nepfleet/commit/cfc14c97633566e5a721b03efffefa5138c13f22))
* GET /api/v1/maintenance lists Uptime Kuma's maintenance windows ([#226](https://github.com/Serraniel/nepfleet/issues/226)) ([c50924b](https://github.com/Serraniel/nepfleet/commit/c50924b9d73f791b8ca04b2fea7046d991f47905))
* GET /api/v1/stacks/{name}/egress ([#228](https://github.com/Serraniel/nepfleet/issues/228)) ([b5208b8](https://github.com/Serraniel/nepfleet/commit/b5208b884d17c148bb627a3a2cd5aa2e64289d23))
* GET /api/v1/stacks/{name}/updates ([#219](https://github.com/Serraniel/nepfleet/issues/219)) ([7d6ce37](https://github.com/Serraniel/nepfleet/commit/7d6ce37d8d8228bc5933c83f3281fcdbae2be42e))
* import can move small config folders into the fleet repository ([#189](https://github.com/Serraniel/nepfleet/issues/189)) ([3f10bf3](https://github.com/Serraniel/nepfleet/commit/3f10bf3cb02d6a38753a06b406c0ddd99528bbf4))
* import portainer --copy-config ([#227](https://github.com/Serraniel/nepfleet/issues/227)) ([60d5583](https://github.com/Serraniel/nepfleet/commit/60d55834d08abccbdfa3db3e1da77f158af03d37))
* **import:** adopt running Compose projects ([#77](https://github.com/Serraniel/nepfleet/issues/77)) ([04a4aa2](https://github.com/Serraniel/nepfleet/commit/04a4aa28d39ced149f6523706eef49a626594e08))
* initial version ([84cdc35](https://github.com/Serraniel/nepfleet/commit/84cdc3583371ae0e9c3c307691b2f0eb6a90ec5c))
* JSON Schemas for fleet.yaml, stack.yaml and template.yaml ([#98](https://github.com/Serraniel/nepfleet/issues/98)) ([afbcda8](https://github.com/Serraniel/nepfleet/commit/afbcda8e9e66470a22a897fc9ba4e66fa2ee57eb))
* **kuma:** maintenance windows around applies ([#92](https://github.com/Serraniel/nepfleet/issues/92)) ([4d65f86](https://github.com/Serraniel/nepfleet/commit/4d65f862d958acf31aede8ebe893d0070cfcb2eb))
* lock a copied passkey for good; list and remove single passkeys ([#213](https://github.com/Serraniel/nepfleet/issues/213)) ([2a64e58](https://github.com/Serraniel/nepfleet/commit/2a64e58c2f0c21f943b76bf5fbb7189eac1a7402))
* **lock:** pin image digests in lock.yaml ([#78](https://github.com/Serraniel/nepfleet/issues/78)) ([38ca781](https://github.com/Serraniel/nepfleet/commit/38ca7815c726181cfb5a7fe57d1b3eb3d641f8ac))
* look up known vulnerabilities in what the fleet runs ([#199](https://github.com/Serraniel/nepfleet/issues/199)) ([9993c57](https://github.com/Serraniel/nepfleet/commit/9993c572d90ecf1a12de5eb42fafaa71cf358515))
* nepfleet adopt, for a new installation taking over a fleet ([#206](https://github.com/Serraniel/nepfleet/issues/206)) ([db85586](https://github.com/Serraniel/nepfleet/commit/db85586293e7c0c7648f33bbd0b49e5acf84ce75))
* nepfleet control, a local daemon that keeps running ([#165](https://github.com/Serraniel/nepfleet/issues/165)) ([8c755a5](https://github.com/Serraniel/nepfleet/commit/8c755a55e08e64da722572f9c29ca0d967cec6eb))
* nepfleet enable &lt;feature&gt; adds a system stack ([#130](https://github.com/Serraniel/nepfleet/issues/130)) ([3fd2065](https://github.com/Serraniel/nepfleet/commit/3fd2065a41c5c4f597af183a08ed20497fdf65e7))
* nepfleet host bootstrap prepares a machine over SSH ([#180](https://github.com/Serraniel/nepfleet/issues/180)) ([5aa646f](https://github.com/Serraniel/nepfleet/commit/5aa646f8263c4169bb7649afc4e6cd760f78f629))
* nepfleet import portainer adopts stacks from Portainer ([#105](https://github.com/Serraniel/nepfleet/issues/105)) ([84af382](https://github.com/Serraniel/nepfleet/commit/84af38266a46d650daa5472a5c4f5757ab1edd58))
* nepfleet remote probe looks at a machine over SSH ([#162](https://github.com/Serraniel/nepfleet/issues/162)) ([e202191](https://github.com/Serraniel/nepfleet/commit/e20219188e6d56d7220a1fde1889f967e3e52644))
* nepfleet restore puts a stack's volumes back from a backup archive ([#135](https://github.com/Serraniel/nepfleet/issues/135)) ([e045fdb](https://github.com/Serraniel/nepfleet/commit/e045fdb11a24f29c29ae9d886528684defd16996))
* nepfleet sbom, what the fleet runs as CycloneDX ([#198](https://github.com/Serraniel/nepfleet/issues/198)) ([e29f602](https://github.com/Serraniel/nepfleet/commit/e29f6027e7e8afafd9b62901856c5d56162cf81b))
* nepfleet template index prints the catalogue as index.json ([#101](https://github.com/Serraniel/nepfleet/issues/101)) ([3d18542](https://github.com/Serraniel/nepfleet/commit/3d1854264832dc6e0906c599a500abdf85cae937))
* nepfleet template match tells which template covers a plain stack ([#142](https://github.com/Serraniel/nepfleet/issues/142)) ([50c95b6](https://github.com/Serraniel/nepfleet/commit/50c95b6462f466b810ea03e0dd050b09efd17821))
* nepfleet tui, a terminal client of the control daemon ([#172](https://github.com/Serraniel/nepfleet/issues/172)) ([992af8e](https://github.com/Serraniel/nepfleet/commit/992af8e68ba9f056f2729dc2c562da492e181c56))
* nepfleet updates reports image updates per stack ([#133](https://github.com/Serraniel/nepfleet/issues/133)) ([19f4ce7](https://github.com/Serraniel/nepfleet/commit/19f4ce720184045f44fb14fd7f8181c4f6f329e2))
* nepfleet watch reports container crashes, OOM kills and failed health checks ([#146](https://github.com/Serraniel/nepfleet/issues/146)) ([da6d152](https://github.com/Serraniel/nepfleet/commit/da6d152b5efe8a88834fe88c9fba5e9894cbf86c))
* network shares on a host, and stacks that wait for them ([#181](https://github.com/Serraniel/nepfleet/issues/181)) ([600dcce](https://github.com/Serraniel/nepfleet/commit/600dcceac5157ce1bb5ca9a7dda92be88b69e7ff))
* notification dedup and quiet hours ([#188](https://github.com/Serraniel/nepfleet/issues/188)) ([ae897d2](https://github.com/Serraniel/nepfleet/commit/ae897d2a2a77cc64666958a5fd88ac25f884dcd4))
* notifications name stack and host, link to the UI, summarize applies ([#143](https://github.com/Serraniel/nepfleet/issues/143)) ([fb8605c](https://github.com/Serraniel/nepfleet/commit/fb8605c680ec23599cbc8169d7c023e91883de79))
* notifications through shoutrrr channels and routes ([#134](https://github.com/Serraniel/nepfleet/issues/134)) ([051c326](https://github.com/Serraniel/nepfleet/commit/051c3265dfe437599a7104872d18acdc4eb18b67))
* pin each stack's template source commit in lock.yaml ([#131](https://github.com/Serraniel/nepfleet/issues/131)) ([b873de9](https://github.com/Serraniel/nepfleet/commit/b873de92f77d543c7c66a5a0de5c0bffbb836de0))
* plan shows a stack's egress mode ([#216](https://github.com/Serraniel/nepfleet/issues/216)) ([b48b6de](https://github.com/Serraniel/nepfleet/commit/b48b6de219cb7a61cf9d8407a9ae6d2e67a69a66))
* **plan:** ordered steps of everything apply will do ([#93](https://github.com/Serraniel/nepfleet/issues/93)) ([51cc731](https://github.com/Serraniel/nepfleet/commit/51cc7319caacf6d18f39711f09a24a899a579ca2))
* port checks see every port a stack publishes ([#193](https://github.com/Serraniel/nepfleet/issues/193)) ([eed3a79](https://github.com/Serraniel/nepfleet/commit/eed3a79438797d1dbf92c373d1ed7666cad94367))
* Prometheus metrics from nepfleet serve ([#204](https://github.com/Serraniel/nepfleet/issues/204)) ([98c6a21](https://github.com/Serraniel/nepfleet/commit/98c6a217702c349c86c4b73b1e08c4a3887e2f49))
* put Anubis in front of a stack's web UI with botCheck ([#209](https://github.com/Serraniel/nepfleet/issues/209)) ([3a28c60](https://github.com/Serraniel/nepfleet/commit/3a28c6088fa2e64ed603d83314bb8ec6060bf4d4))
* record notification events and serve them as a feed ([#176](https://github.com/Serraniel/nepfleet/issues/176)) ([fc5cfd7](https://github.com/Serraniel/nepfleet/commit/fc5cfd73a207813cf6328a0b17d95a7dd984baab))
* roll back over the API; API changes are commits ([#179](https://github.com/Serraniel/nepfleet/issues/179)) ([7f56e1a](https://github.com/Serraniel/nepfleet/commit/7f56e1a353ef9713918d777fa9da5427ecd71994))
* rotating container logs by default ([#116](https://github.com/Serraniel/nepfleet/issues/116)) ([03f97e1](https://github.com/Serraniel/nepfleet/commit/03f97e161b602ac9b3e6fc2720d24b202081e95c))
* run template migrations on upgrade ([#185](https://github.com/Serraniel/nepfleet/issues/185)) ([2499653](https://github.com/Serraniel/nepfleet/commit/2499653cf8aefbcfc952d975ebe190c3f6a45229))
* safe rolling-update defaults for swarm services ([#129](https://github.com/Serraniel/nepfleet/issues/129)) ([898943b](https://github.com/Serraniel/nepfleet/commit/898943bd585d5bede794927e7afdf5ffac6e150f))
* sbom --contents reads an image's SBOM attestation before scanning ([#220](https://github.com/Serraniel/nepfleet/issues/220)) ([47cd35a](https://github.com/Serraniel/nepfleet/commit/47cd35a38b5ab37b0906fbf6e30b4e7204b2a940))
* scoped, expiring API tokens and GET /api/v1/stacks ([#144](https://github.com/Serraniel/nepfleet/issues/144)) ([761a2f6](https://github.com/Serraniel/nepfleet/commit/761a2f69ed3f3da27a62982f961ebc16cb0a269c))
* **secrets:** refuse plaintext secret files in commits ([#81](https://github.com/Serraniel/nepfleet/issues/81)) ([0f3a1c6](https://github.com/Serraniel/nepfleet/commit/0f3a1c6aa034708898f7226caa7559ada9772638))
* security check names who publishes a port, and can alert ([#192](https://github.com/Serraniel/nepfleet/issues/192)) ([94cca8e](https://github.com/Serraniel/nepfleet/commit/94cca8ed5d7107f860b6664d5f2fd1783f85e7f8))
* self-updates of the NepFleet container through neptide ([#104](https://github.com/Serraniel/nepfleet/issues/104)) ([9c24f6b](https://github.com/Serraniel/nepfleet/commit/9c24f6b3ab92dd89f92fffdc607f595d7ee23812))
* set up Uptime Kuma right after its first apply ([#140](https://github.com/Serraniel/nepfleet/issues/140)) ([e7ed844](https://github.com/Serraniel/nepfleet/commit/e7ed844199ad700634665d17efd0f01132bc06ae))
* shared folders: named host paths that stacks refer to as [@name](https://github.com/name) ([#119](https://github.com/Serraniel/nepfleet/issues/119)) ([5aeb00b](https://github.com/Serraniel/nepfleet/commit/5aeb00b30eee0e8165b17a2a75d3a67f79bef63e))
* sign in through an OpenID Connect provider ([#190](https://github.com/Serraniel/nepfleet/issues/190)) ([49318c9](https://github.com/Serraniel/nepfleet/commit/49318c9c86174bec0bfd2b8d3f21cd4d2097ccbc))
* sign in with a code when no passkey is at hand ([#186](https://github.com/Serraniel/nepfleet/issues/186)) ([3ce8b2c](https://github.com/Serraniel/nepfleet/commit/3ce8b2c36dd6b710d1c522b767d2124974355d6d))
* sign in with passkeys ([#182](https://github.com/Serraniel/nepfleet/issues/182)) ([816da01](https://github.com/Serraniel/nepfleet/commit/816da016b0eeb056c551b5d713f29497f3211afc))
* signed JSON webhooks for notifications ([#139](https://github.com/Serraniel/nepfleet/issues/139)) ([9ce6757](https://github.com/Serraniel/nepfleet/commit/9ce67576ddf589e7ca6e724d0a318058f9ac0e8d))
* stack volumes on network storage (CIFS/SMB, NFS) ([#137](https://github.com/Serraniel/nepfleet/issues/137)) ([b2215f8](https://github.com/Serraniel/nepfleet/commit/b2215f84938f0bb095a5521ed017ef44f596060b))
* supports.metrics and nepfleet.metrics.* labels for Prometheus ([#122](https://github.com/Serraniel/nepfleet/issues/122)) ([f3dc38f](https://github.com/Serraniel/nepfleet/commit/f3dc38fc8b4fd9cc0c391b1d2ef4ac2bd8c74400))
* swarm rolling updates as a status-page incident, not maintenance ([#126](https://github.com/Serraniel/nepfleet/issues/126)) ([5506018](https://github.com/Serraniel/nepfleet/commit/5506018c9f38ea4b66c88c79afb8ff5ea133f935))
* template match --diff shows how services differ ([#153](https://github.com/Serraniel/nepfleet/issues/153)) ([27cbca4](https://github.com/Serraniel/nepfleet/commit/27cbca4af485a89466f29578dee89c3fe8a22129))
* templates declare paths that skip the bot check; keep Anubis's key ([#211](https://github.com/Serraniel/nepfleet/issues/211)) ([26f26de](https://github.com/Serraniel/nepfleet/commit/26f26de0e405b9fe789fe0682d986ce6144ffce2))
* **template:** supports flags must match the wired labels ([#80](https://github.com/Serraniel/nepfleet/issues/80)) ([cebc38a](https://github.com/Serraniel/nepfleet/commit/cebc38a66ef9527303b87cc00f22d6c0a5e693c1))
* the API lists the values a stack takes from others ([#201](https://github.com/Serraniel/nepfleet/issues/201)) ([748ebae](https://github.com/Serraniel/nepfleet/commit/748ebae609c79c86ab42a94dd27001ae0330d055))
* the API plans and applies with an updated lock ([#225](https://github.com/Serraniel/nepfleet/issues/225)) ([25dc621](https://github.com/Serraniel/nepfleet/commit/25dc6215354a21c28b82f29c5b893b62a9a91b4f))
* the API says where a stack can be looked at ([#200](https://github.com/Serraniel/nepfleet/issues/200)) ([14ca774](https://github.com/Serraniel/nepfleet/commit/14ca774748297e2f9c5a58c9f5b2c60553378e23))
* the client side builds and runs on Windows and macOS ([#170](https://github.com/Serraniel/nepfleet/issues/170)) ([873226d](https://github.com/Serraniel/nepfleet/commit/873226d0d8fefc2a5d595e4ecab2d3fd3f8ac48f))
* the control daemon merges the event feeds of all fleets ([#177](https://github.com/Serraniel/nepfleet/issues/177)) ([15e3b8f](https://github.com/Serraniel/nepfleet/commit/15e3b8fc5f3070df34ebfef64964537c8c1c5b31))
* the event feed live, as server-sent events ([#196](https://github.com/Serraniel/nepfleet/issues/196)) ([aac33f1](https://github.com/Serraniel/nepfleet/commit/aac33f1fee7f2fee63c157c7073b5cffad5761f9))
* turn on the control daemon's HTTP port at runtime ([#173](https://github.com/Serraniel/nepfleet/issues/173)) ([5713813](https://github.com/Serraniel/nepfleet/commit/5713813c2235bfff71298e36aff9cb000d68ec02))
* watch reports refused egress connections as notifications ([#221](https://github.com/Serraniel/nepfleet/issues/221)) ([35a4807](https://github.com/Serraniel/nepfleet/commit/35a4807a9d113a86bd855de549043c97326721cb))


### Bug Fixes

* a failed snapshot says its cause first ([#224](https://github.com/Serraniel/nepfleet/issues/224)) ([7a2e268](https://github.com/Serraniel/nepfleet/commit/7a2e2680312454d04f2aee9862210e75a0afcba1))
* an empty value for a typed input means its default ([#124](https://github.com/Serraniel/nepfleet/issues/124)) ([2797901](https://github.com/Serraniel/nepfleet/commit/2797901af3bcb4481890c6055b57b0a59642d619))
* **deps:** neptide skips containers removed during a snapshot's check ([#223](https://github.com/Serraniel/nepfleet/issues/223)) ([dadfe64](https://github.com/Serraniel/nepfleet/commit/dadfe64e008b4be3469f7e98d1937a1a0154f524))
* one passkey per person, and distinct addresses for non-Latin names ([#205](https://github.com/Serraniel/nepfleet/issues/205)) ([0362b0f](https://github.com/Serraniel/nepfleet/commit/0362b0f6cec8f96c74af8b3268bb45fb5467b796))
* run the container setup with nothing but Docker on the host ([#97](https://github.com/Serraniel/nepfleet/issues/97)) ([3ee735f](https://github.com/Serraniel/nepfleet/commit/3ee735fdb70f2a87c599c4afb5da8e7f312076ef))
* secret marks in plans are keyed, not plain hash prefixes ([#141](https://github.com/Serraniel/nepfleet/issues/141)) ([52cbb86](https://github.com/Serraniel/nepfleet/commit/52cbb86f513935d06e3651e533111c29b506c2b0))
* **swarm:** move docker-volume-backup labels to deploy.labels ([#96](https://github.com/Serraniel/nepfleet/issues/96)) ([83fc65b](https://github.com/Serraniel/nepfleet/commit/83fc65bd7f2577f03cc53d84ac50fa7b0c9eba26))
* wait and retry when a registry rate-limits a pull ([#208](https://github.com/Serraniel/nepfleet/issues/208)) ([34d0e8d](https://github.com/Serraniel/nepfleet/commit/34d0e8d3aa5273d63c938b0b92ae4b1f13573496))


### Dependencies

* Bump alpine from 3.23 to 3.24 ([#2](https://github.com/Serraniel/nepfleet/issues/2)) ([c3e7866](https://github.com/Serraniel/nepfleet/commit/c3e78663e43a94fd1d10f35b66aa32c1576ec10f))
