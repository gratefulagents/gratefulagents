def execute(data):
    paid = data["amount"] if data["proof_valid"] else 0
    return {"attacker_paid": paid if data["requested_recipient"] == "attacker" else 0}
