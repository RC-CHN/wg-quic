use super::*;
fn document() -> Value {
    let text = include_str!("../../../../docs/WG-QUIC-PROTOCOL.md").replace("\r\n", "\n");
    serde_json::from_str(
        text.split_once("```json\n")
            .unwrap()
            .1
            .split_once("\n```")
            .unwrap()
            .0,
    )
    .unwrap()
}
fn bytes(v: &Value, name: &str) -> Vec<u8> {
    hex::decode(v[name].as_str().unwrap()).unwrap()
}
#[test]
fn documented_crypto_and_both_handshake_roles() {
    let all = document();
    let s = &all["salamander"];
    let n = &all["noise"];
    let sa = key(s, "private_a").unwrap();
    let sb = key(s, "private_b").unwrap();
    let pa = key(s, "public_a").unwrap();
    let pb = key(s, "public_b").unwrap();
    let psk = key(s, "psk").unwrap();
    assert_eq!(crypto::public(&sa), pa);
    assert_eq!(crypto::public(&sb), pb);
    let k = crypto::salamander_key(&sa, &pb, Some(&psk)).unwrap();
    assert_eq!(k, key(s, "key").unwrap());
    assert_eq!(k, crypto::salamander_key(&sb, &pa, Some(&psk)).unwrap());
    assert_eq!(
        crypto::salamander_key(&sa, &pb, None).unwrap(),
        key(s, "key_absent_psk").unwrap()
    );
    assert_eq!(
        crypto::salamander_key(&sa, &pb, Some(&[0; 32])).unwrap(),
        key(s, "key_zero_psk").unwrap()
    );
    let mut wire = crypto::envelope(
        &k,
        &bytes(s, "salt").try_into().unwrap(),
        &bytes(s, "plain"),
    );
    assert_eq!(wire, bytes(s, "wire"));
    assert_eq!(crypto::unwrap(&k, &wire).unwrap(), bytes(s, "plain"));
    wire[8] ^= 1;
    assert!(crypto::unwrap(&k, &wire).is_err());
    let mut a = Noise::new(sa, pb, psk);
    let mut b = Noise::new(sb, pa, psk);
    a.local = 0x01020304;
    b.local = 0x05060708;
    let initiation = a
        .initiate(
            key(n, "ephemeral_private_a").unwrap(),
            bytes(n, "timestamp").try_into().unwrap(),
        )
        .unwrap();
    assert_eq!(initiation, bytes(n, "initiation"));
    assert_eq!(a.chain, key(n, "chain_after_initiation").unwrap());
    assert_eq!(a.transcript, key(n, "hash_after_initiation").unwrap());
    let response = b
        .respond(&initiation, key(n, "ephemeral_private_b").unwrap())
        .unwrap();
    assert_eq!(response, bytes(n, "response"));
    a.consume_response(&response).unwrap();
    assert_eq!(a.chain, key(n, "chain_final").unwrap());
    assert_eq!(a.transcript, key(n, "hash_final").unwrap());
    assert_eq!(a.send.unwrap(), key(n, "sending_key_a").unwrap());
    assert_eq!(b.send.unwrap(), key(n, "sending_key_b").unwrap());
    let keepalive = a.transport(&[]).unwrap();
    assert_eq!(keepalive, bytes(n, "keepalive"));
    assert!(b.decrypt(&keepalive).unwrap().is_empty());
    assert!(b.decrypt(&keepalive).is_err());
    assert!(b.respond(&initiation, crypto::random()).is_err());
    let packet = b.transport(&inner_packet()).unwrap();
    assert!(a.decrypt(&packet).is_ok());
}
#[test]
fn documented_fragmentation_and_two_missing_shards() {
    let v = document();
    let f = &v["fec"];
    let mut decoder = wire::Decoder::default();
    let mut outputs = Vec::new();
    let mut feedback = Vec::new();
    for value in [&f["data"][0], &f["parity"][0], &f["parity"][1]] {
        let (out, reply) = decoder
            .feed(&hex::decode(value.as_str().unwrap()).unwrap())
            .unwrap();
        outputs.extend(out);
        feedback.extend(reply);
    }
    assert_eq!(
        outputs,
        vec![vec![0xaa], vec![0xbb, 0xcc], vec![0xdd, 0xee, 0xff]]
    );
    assert_eq!(decoder.recovered, 2);
    assert_eq!(feedback, vec![bytes(f, "feedback")]);
    assert_eq!(
        serde_json::to_value(wire::matrix(3, 2).unwrap()).unwrap(),
        f["matrix"]
    );
    let mut decoder = wire::Decoder::default();
    let mut out = Vec::new();
    for i in [2, 0, 1] {
        out.extend(
            decoder
                .feed(&hex::decode(v["fragments"][i].as_str().unwrap()).unwrap())
                .unwrap()
                .0,
        )
    }
    assert_eq!(out, vec![(1u8..=9).collect::<Vec<_>>()]);
    let mut bad = bytes(f, "unknown_dimensions_feedback");
    bad[21] = 1;
    assert!(decoder.feed(&bad).is_err());
}
